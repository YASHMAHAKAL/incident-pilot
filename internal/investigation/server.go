package investigation

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"incidentpilot/internal/remediation"
)

type WorkloadInput struct {
	Workload string `json:"workload" jsonschema:"One of frontend, orders-api, or payments-api"`
}
type LogsInput struct {
	Workload string `json:"workload" jsonschema:"Demo workload name"`
	Pod      string `json:"pod" jsonschema:"Pod name within the workload"`
	Lines    int    `json:"lines" jsonschema:"Number of lines, 1 to 100"`
}
type MetricInput struct {
	Workload string `json:"workload" jsonschema:"Demo workload name"`
	Metric   string `json:"metric" jsonschema:"One of request_rate, error_rate, heap_bytes, or success_latency_avg"`
}
type LokiInput struct {
	Workload string `json:"workload" jsonschema:"Demo workload name"`
	Minutes  int    `json:"minutes,omitempty" jsonschema:"Relative window in minutes, 1 to 60; omit when start and end are given"`
	Start    string `json:"start,omitempty" jsonschema:"Optional RFC3339 window start"`
	End      string `json:"end,omitempty" jsonschema:"Optional RFC3339 window end"`
	Limit    int    `json:"limit" jsonschema:"Maximum log entries, 1 to 100"`
}
type MetricWindowInput struct {
	Workload string `json:"workload" jsonschema:"Demo workload name"`
	Metric   string `json:"metric" jsonschema:"One of request_rate, error_rate, heap_bytes, or success_latency_avg"`
	Start    string `json:"start" jsonschema:"RFC3339 window start"`
	End      string `json:"end" jsonschema:"RFC3339 window end"`
}
type TraceInput struct {
	TraceID string `json:"trace_id" jsonschema:"16 to 32 hexadecimal characters; Tempo may omit leading zeroes"`
}
type TraceSearchInput struct {
	Workload string `json:"workload" jsonschema:"Demo workload name"`
	Filter   string `json:"filter,omitempty" jsonschema:"Optional fixed filter: error or slow_success"`
	Minutes  int    `json:"minutes,omitempty" jsonschema:"Relative window in minutes, 1 to 60; omit when start and end are given"`
	Start    string `json:"start,omitempty" jsonschema:"Optional RFC3339 window start"`
	End      string `json:"end,omitempty" jsonschema:"Optional RFC3339 window end"`
}
type RevisionHistoryInput struct {
	Start string `json:"start" jsonschema:"RFC3339 incident window start"`
	End   string `json:"end" jsonschema:"RFC3339 incident window end"`
}
type RevisionInput struct {
	Revision string `json:"revision" jsonschema:"Hexadecimal commit revision selected from Argo CD history"`
	Start    string `json:"start" jsonschema:"RFC3339 incident window start"`
	End      string `json:"end" jsonschema:"RFC3339 incident window end"`
}

func parseWindow(startValue, endValue string) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, startValue)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid RFC3339 start")
	}
	end, err := time.Parse(time.RFC3339, endValue)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid RFC3339 end")
	}
	return start, end, ValidateWindow(start, end)
}

type Result struct {
	Source      string    `json:"source"`
	CollectedAt time.Time `json:"collected_at"`
	Data        any       `json:"data,omitempty"`
	Text        string    `json:"text,omitempty"`
}

type Server struct {
	backend    Backend
	remediator RemediationRequester
	token      string
	logger     *slog.Logger
	calls      metric.Int64Counter
}

type RemediationRequester interface {
	Request(context.Context, remediation.Request) (remediation.Result, error)
}

func NewServer(backend Backend, token string, logger *slog.Logger) (*Server, error) {
	return NewServerWithRemediation(backend, nil, token, logger)
}

func NewServerWithRemediation(backend Backend, remediator RemediationRequester, token string, logger *slog.Logger) (*Server, error) {
	if len(token) < 24 {
		return nil, errors.New("MCP bearer token must be at least 24 characters")
	}
	if logger == nil {
		logger = slog.Default()
	}
	calls, err := otel.Meter("incidentpilot/mcp").Int64Counter("incidentpilot_tool_calls_total")
	if err != nil {
		return nil, err
	}
	return &Server{backend: backend, remediator: remediator, token: token, logger: logger, calls: calls}, nil
}

func (s *Server) instrument(ctx context.Context, name string, f func(context.Context) (Result, error)) (*mcp.CallToolResult, Result, error) {
	ctx, span := otel.Tracer("incidentpilot/mcp").Start(ctx, "mcp."+name)
	defer span.End()
	started := time.Now()
	result, err := f(ctx)
	if raw, ok := result.Data.(json.RawMessage); ok && err == nil {
		var decoded any
		if decodeErr := json.Unmarshal(raw, &decoded); decodeErr != nil {
			err = decodeErr
		} else {
			result.Data = decoded
		}
	}
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
	}
	span.SetAttributes(attribute.String("mcp.tool", name), attribute.String("mcp.status", status))
	s.calls.Add(ctx, 1, metric.WithAttributes(attribute.String("tool", name), attribute.String("status", status)))
	s.logger.InfoContext(ctx, "mcp tool", "tool", name, "status", status, "duration_ms", time.Since(started).Milliseconds())
	return nil, result, err
}

func (s *Server) Handler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "incidentpilot-mcp", Version: "0.1.0"}, nil)
	readonly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	nondestructive := false
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_deployment", Description: "Read an allowlisted demo Deployment in incidentpilot-demo.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in WorkloadInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_deployment", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Deployment(ctx, in.Workload)
			return Result{Source: "kubernetes/deployment", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_orders_config", Description: "Read only orders-config.data.order_mode from the demo namespace; this is not a generic ConfigMap or Secret reader.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_orders_config", func(ctx context.Context) (Result, error) {
			data, err := s.backend.OrdersConfig(ctx)
			return Result{Source: "kubernetes/configmap", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_service", Description: "Read an allowlisted demo Service and its selector.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in WorkloadInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_service", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Service(ctx, in.Workload)
			return Result{Source: "kubernetes/service", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_endpointslices", Description: "Read up to 20 EndpointSlices selected by one allowlisted demo Service.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in WorkloadInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_endpointslices", func(ctx context.Context) (Result, error) {
			data, err := s.backend.EndpointSlices(ctx, in.Workload)
			return Result{Source: "kubernetes/endpointslices", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_pods", Description: "Read status of up to 20 demo workload pods.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in WorkloadInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_pods", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Pods(ctx, in.Workload)
			return Result{Source: "kubernetes/pods", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_events", Description: "Read bounded recent events in the demo namespace.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in WorkloadInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_events", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Events(ctx, in.Workload)
			return Result{Source: "kubernetes/events", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "kubernetes_get_pod_logs", Description: "Read at most 100 lines and 64 KiB from one demo pod.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in LogsInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "kubernetes_get_pod_logs", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Logs(ctx, in.Workload, in.Pod, in.Lines)
			return Result{Source: "kubernetes/pod_logs", CollectedAt: time.Now().UTC(), Text: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "prometheus_get_demo_metric", Description: "Query one fixed demo metric expression (request_rate, error_rate, heap_bytes, success_latency_avg).", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in MetricInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "prometheus_get_demo_metric", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Metric(ctx, in.Workload, in.Metric)
			return Result{Source: "prometheus", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "prometheus_get_demo_metric_range", Description: "Query a fixed demo metric over a bounded RFC3339 incident window.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in MetricWindowInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "prometheus_get_demo_metric_range", func(ctx context.Context) (Result, error) {
			start, end, err := parseWindow(in.Start, in.End)
			if err != nil {
				return Result{}, err
			}
			data, err := s.backend.MetricWindow(ctx, in.Workload, in.Metric, start, end)
			return Result{Source: "prometheus/range", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "loki_get_demo_logs", Description: "Read up to 100 log entries from a demo service over at most 60 minutes.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in LokiInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "loki_get_demo_logs", func(ctx context.Context) (Result, error) {
			var data json.RawMessage
			var err error
			if in.Start != "" || in.End != "" {
				if in.Minutes != 0 {
					return Result{}, errors.New("use either minutes or start/end")
				}
				start, end, parseErr := parseWindow(in.Start, in.End)
				if parseErr != nil {
					return Result{}, parseErr
				}
				data, err = s.backend.LogWindow(ctx, in.Workload, start, end, in.Limit)
			} else {
				data, err = s.backend.LogQuery(ctx, in.Workload, in.Minutes, in.Limit)
			}
			return Result{Source: "loki", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "tempo_search_demo_traces", Description: "Find up to 10 traces for one demo workload in at most 60 minutes.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in TraceSearchInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "tempo_search_demo_traces", func(ctx context.Context) (Result, error) {
			var data json.RawMessage
			var err error
			if in.Start != "" || in.End != "" {
				if in.Minutes != 0 {
					return Result{}, errors.New("use either minutes or start/end")
				}
				start, end, parseErr := parseWindow(in.Start, in.End)
				if parseErr != nil {
					return Result{}, parseErr
				}
				data, err = s.backend.SearchTracesWindowFiltered(ctx, in.Workload, in.Filter, start, end)
			} else {
				data, err = s.backend.SearchTracesFiltered(ctx, in.Workload, in.Filter, in.Minutes)
			}
			return Result{Source: "tempo/search", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "tempo_get_trace", Description: "Read a single trace by hex trace ID (up to 256 KiB).", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in TraceInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "tempo_get_trace", func(ctx context.Context) (Result, error) {
			data, err := s.backend.Trace(ctx, in.TraceID)
			return Result{Source: "tempo", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "argocd_get_application", Description: "Read the fixed configured Argo CD application's sync and health state.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "argocd_get_application", func(ctx context.Context) (Result, error) {
			data, err := s.backend.argoApplication(ctx)
			return Result{Source: "argocd/application", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "argocd_get_revision_history", Description: "Read recent revisions of the fixed Argo CD application around a bounded incident window.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in RevisionHistoryInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "argocd_get_revision_history", func(ctx context.Context) (Result, error) {
			start, end, err := parseWindow(in.Start, in.End)
			if err != nil {
				return Result{}, err
			}
			data, err := s.backend.argoRevisionHistory(ctx, start, end)
			return Result{Source: "argocd/revision_history", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "github_get_commit", Description: "Read bounded metadata for one commit selected from Argo CD history in the fixed configured repository.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in RevisionInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "github_get_commit", func(ctx context.Context) (Result, error) {
			start, end, err := parseWindow(in.Start, in.End)
			if err != nil {
				return Result{}, err
			}
			data, err := s.backend.githubCommit(ctx, in.Revision, start, end)
			return Result{Source: "github/commit", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "github_get_diff", Description: "Read at most 30 changed files and bounded patches for one commit selected from Argo CD history in the fixed configured repository.", Annotations: readonly}, func(ctx context.Context, _ *mcp.CallToolRequest, in RevisionInput) (*mcp.CallToolResult, Result, error) {
		return s.instrument(ctx, "github_get_diff", func(ctx context.Context) (Result, error) {
			start, end, err := parseWindow(in.Start, in.End)
			if err != nil {
				return Result{}, err
			}
			data, err := s.backend.githubDiff(ctx, in.Revision, start, end)
			return Result{Source: "github/diff", CollectedAt: time.Now().UTC(), Data: data}, err
		})
	})
	if s.remediator != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "request_remediation",
			Description: "Submit one evidence-linked remediation proposal for trusted validation, OPA policy evaluation, and durable audit. This does not mutate Kubernetes or create a pull request.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nondestructive, OpenWorldHint: &nondestructive},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in remediation.Request) (*mcp.CallToolResult, remediation.Result, error) {
			ctx, span := otel.Tracer("incidentpilot/mcp").Start(ctx, "mcp.request_remediation")
			defer span.End()
			started := time.Now()
			result, err := s.remediator.Request(ctx, in)
			status := "ok"
			if err != nil {
				status = "error"
				span.RecordError(err)
			}
			span.SetAttributes(attribute.String("mcp.tool", "request_remediation"), attribute.String("mcp.status", status))
			s.calls.Add(ctx, 1, metric.WithAttributes(attribute.String("tool", "request_remediation"), attribute.String("status", status)))
			s.logger.InfoContext(ctx, "mcp tool", "tool", "request_remediation", "status", status, "duration_ms", time.Since(started).Milliseconds())
			return nil, result, err
		})
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	return mux
}
