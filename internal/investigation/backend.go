package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"incidentpilot/internal/onboarding"
)

// Backend contains only fixed, read-only upstreams. No tool accepts an URL or query language.
type Backend struct {
	Profile                             onboarding.Profile
	Client                              *http.Client
	Kubernetes, Prometheus, Loki, Tempo string
	Token                               string
	ArgoCD                              string
	ArgoCDToken                         string
	ArgoCDApplication                   string
	ArgoCDProject                       string
	GitHub                              string
	GitHubToken                         string
	GitHubRepository                    string
}

func (b Backend) get(ctx context.Context, base, path string, query url.Values, kubernetes bool, limit int64) (json.RawMessage, error) {
	headers := make(http.Header)
	if kubernetes {
		headers.Set("Authorization", "Bearer "+b.Token)
	}
	return b.getJSON(ctx, base, path, query, headers, limit)
}

func (b Backend) getJSON(ctx context.Context, base, path string, query url.Values, headers http.Header, limit int64) (json.RawMessage, error) {
	if b.Client == nil {
		return nil, errors.New("HTTP client is not configured")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("upstream is not configured")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errors.New("upstream response exceeds size limit")
	}
	if !json.Valid(body) {
		return nil, errors.New("upstream returned invalid JSON")
	}
	return body, nil
}

func (b Backend) workload(name string) bool { return b.Profile.AllowsWorkload(name) }
func (b Backend) namespace() string         { return b.Profile.Effective().Namespace }

func (b Backend) Deployment(ctx context.Context, name string) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/apis/apps/v1/namespaces/"+b.namespace()+"/deployments/"+name, nil, true, 256<<10)
	if err != nil {
		return nil, err
	}
	clean, err := sanitizeKubernetes(raw, nil)
	if err != nil {
		return clean, err
	}
	if b.Profile.Effective().Mode == "external" {
		return clean, nil
	}
	return addDiagnosticDeploymentConfig(clean, raw, name)
}

func addDiagnosticDeploymentConfig(clean, raw json.RawMessage, workloadName string) (json.RawMessage, error) {
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name      string `json:"name"`
							Value     string `json:"value"`
							ValueFrom struct {
								ConfigMapKeyRef struct {
									Name string `json:"name"`
									Key  string `json:"key"`
								} `json:"configMapKeyRef"`
							} `json:"valueFrom"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	var projected map[string]any
	if json.Unmarshal(raw, &deployment) != nil || json.Unmarshal(clean, &projected) != nil {
		return nil, errors.New("invalid Deployment response")
	}
	var runtime []map[string]string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != workloadName {
			continue
		}
		for _, variable := range container.Env {
			ref := variable.ValueFrom.ConfigMapKeyRef
			if workloadName == "orders-api" && variable.Name == "DEMO_ORDER_MODE" && ref.Name == "orders-config" && ref.Key == "order_mode" {
				projected["diagnosticConfigReferences"] = []map[string]string{{"container": "orders-api", "environmentVariable": "DEMO_ORDER_MODE", "configMap": "orders-config", "key": "order_mode"}}
			}
			if workloadName == "orders-api" && variable.Name == "DEMO_ORDER_CPU_BURN_MS" && (variable.Value == "0" || variable.Value == "1000") {
				runtime = append(runtime, map[string]string{"container": "orders-api", "environmentVariable": variable.Name, "value": variable.Value})
			}
			if workloadName == "payments-api" && variable.Name == "DEMO_PAYMENT_MODE" && (variable.Value == "normal" || variable.Value == "fail") {
				runtime = append(runtime, map[string]string{"container": "payments-api", "environmentVariable": variable.Name, "value": variable.Value})
			}
		}
	}
	if len(runtime) > 0 {
		projected["diagnosticRuntimeConfig"] = runtime
	}
	return json.Marshal(projected)
}

// OrdersConfig returns one explicitly accepted, non-secret diagnostic key. It
// is intentionally not a generic ConfigMap reader.
func (b Backend) OrdersConfig(ctx context.Context) (json.RawMessage, error) {
	if b.Profile.Effective().Mode != "demo" {
		return nil, errors.New("demo ConfigMap tool is disabled for external profiles")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/api/v1/namespaces/"+b.namespace()+"/configmaps/orders-config", nil, true, 16<<10)
	if err != nil {
		return nil, err
	}
	var config struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(raw, &config) != nil || config.Metadata.Name != "orders-config" {
		return nil, errors.New("invalid orders ConfigMap response")
	}
	mode := config.Data["order_mode"]
	if len(mode) == 0 || len(mode) > 64 || !utf8.ValidString(mode) {
		return nil, errors.New("invalid order_mode value")
	}
	return json.Marshal(map[string]any{"metadata": map[string]string{"name": "orders-config"}, "data": map[string]string{"order_mode": mode}})
}

func (b Backend) Service(ctx context.Context, name string) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/api/v1/namespaces/"+b.namespace()+"/services/"+name, nil, true, 64<<10)
	return sanitizeKubernetes(raw, err)
}

func (b Backend) EndpointSlices(ctx context.Context, name string) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/apis/discovery.k8s.io/v1/namespaces/"+b.namespace()+"/endpointslices", url.Values{"labelSelector": {"kubernetes.io/service-name=" + name}, "limit": {"20"}}, true, 128<<10)
	return sanitizeKubernetes(raw, err)
}

func (b Backend) Pods(ctx context.Context, name string) (json.RawMessage, error) {
	selector, ok := b.Profile.PodSelector(name)
	if !ok {
		return nil, errors.New("unknown demo workload")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/api/v1/namespaces/"+b.namespace()+"/pods", url.Values{"labelSelector": {selector}, "limit": {"20"}}, true, 256<<10)
	return sanitizeKubernetes(raw, err)
}

func (b Backend) Events(ctx context.Context, name string) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	raw, err := b.get(ctx, b.Kubernetes, "/api/v1/namespaces/"+b.namespace()+"/events", url.Values{"limit": {"100"}}, true, 256<<10)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	filtered := make([]map[string]any, 0, len(list.Items))
	for _, item := range list.Items {
		obj, _ := item["involvedObject"].(map[string]any)
		objectName, _ := obj["name"].(string)
		if objectName == name || strings.HasPrefix(objectName, name+"-") {
			filtered = append(filtered, item)
		}
	}
	return sanitizeKubernetes(jsonFromAny(map[string]any{"items": filtered}), nil)
}

func jsonFromAny(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

// Kubernetes responses are untrusted. Strip credentials and annotation payloads
// without losing workload state needed to diagnose the local scenarios.
func sanitizeKubernetes(raw json.RawMessage, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	var obj any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return json.Marshal(scrub(obj))
}

func scrub(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			switch key {
			case "annotations", "managedFields", "env", "envFrom", "secretKeyRef", "imagePullSecrets", "volumes", "volumeMounts", "data", "stringData", "token", "password":
				continue
			}
			out[key] = scrub(child)
		}
		return out
	case []any:
		for i := range value {
			value[i] = scrub(value[i])
		}
	}
	return v
}

func (b Backend) Logs(ctx context.Context, name, pod string, lines int) (string, error) {
	if !b.workload(name) || !validPod(pod, name) {
		return "", errors.New("invalid scoped pod")
	}
	if lines < 1 || lines > 100 {
		return "", errors.New("lines must be between 1 and 100")
	}
	if b.Profile.Effective().Mode == "external" {
		key, value, _ := b.Profile.PodLabel(name)
		raw, err := b.get(ctx, b.Kubernetes, "/api/v1/namespaces/"+b.namespace()+"/pods/"+pod, nil, true, 64<<10)
		if err != nil {
			return "", err
		}
		var identity struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.Metadata.Labels[key] != value {
			return "", errors.New("pod does not match the configured selector")
		}
	}
	u, err := url.Parse(b.Kubernetes)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("Kubernetes upstream is not configured")
	}
	u.Path = "/api/v1/namespaces/" + b.namespace() + "/pods/" + pod + "/log"
	u.RawQuery = url.Values{"tailLines": {fmt.Sprint(lines)}, "limitBytes": {"65536"}, "timestamps": {"true"}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := b.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Kubernetes logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Kubernetes logs returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return "", err
	}
	if len(body) > 65536 {
		return "", errors.New("logs exceed size limit")
	}
	return string(body), nil
}

func validPod(pod, workloadName string) bool {
	if !strings.HasPrefix(pod, workloadName+"-") || len(pod) > 63 {
		return false
	}
	for _, r := range pod {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func (b Backend) Metric(ctx context.Context, name, metric string) (json.RawMessage, error) {
	if !b.workload(name) || b.Profile.Effective().Mode != "demo" {
		return nil, errors.New("unknown demo workload")
	}
	query, err := metricQuery(name, metric)
	if err != nil {
		return nil, err
	}
	return b.get(ctx, b.Prometheus, "/api/v1/query", url.Values{"query": {query}}, false, 64<<10)
}

func metricQuery(name, metric string) (string, error) {
	var query string
	switch metric {
	case "request_rate":
		query = fmt.Sprintf(`sum(rate(demo_requests_total{service_name=%q}[5m]))`, name)
	case "error_rate":
		query = fmt.Sprintf(`sum(rate(demo_requests_total{service_name=%q,outcome!="success"}[5m]))`, name)
	case "heap_bytes":
		query = fmt.Sprintf(`max(demo_process_heap_alloc_bytes{service_name=%q})`, name)
	case "success_latency_avg":
		query = fmt.Sprintf(`sum(increase(demo_request_duration_seconds_sum{service_name=%q,outcome="success"}[30s])) / sum(increase(demo_request_duration_seconds_count{service_name=%q,outcome="success"}[30s]))`, name, name)
	default:
		return "", errors.New("unknown metric")
	}
	return query, nil
}

func (b Backend) MetricWindow(ctx context.Context, name, metric string, start, end time.Time) (json.RawMessage, error) {
	if !b.workload(name) || b.Profile.Effective().Mode != "demo" {
		return nil, errors.New("unknown demo workload")
	}
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	query, err := metricQuery(name, metric)
	if err != nil {
		return nil, err
	}
	q := url.Values{"query": {query}, "start": {start.UTC().Format(time.RFC3339)}, "end": {end.UTC().Format(time.RFC3339)}, "step": {"60s"}}
	return b.get(ctx, b.Prometheus, "/api/v1/query_range", q, false, 64<<10)
}

// ValidateWindow limits historical telemetry reads to one hour within the last day.
func ValidateWindow(start, end time.Time) error {
	if start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > time.Hour || start.Before(time.Now().Add(-24*time.Hour)) || end.After(time.Now().Add(30*time.Second)) {
		return errors.New("time window must be positive, at most one hour, and within the last 24 hours")
	}
	return nil
}

func (b Backend) LogQuery(ctx context.Context, name string, minutes, limit int) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	if minutes < 1 || minutes > 60 || limit < 1 || limit > 100 {
		return nil, errors.New("log window or limit out of range")
	}
	now := time.Now()
	return b.LogWindow(ctx, name, now.Add(-time.Duration(minutes)*time.Minute), now, limit)
}

func (b Backend) LogWindow(ctx context.Context, name string, start, end time.Time, limit int) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("log limit out of range")
	}
	selector := fmt.Sprintf(`{service_name=%q}`, name)
	if b.Profile.Effective().Mode == "external" {
		selector = fmt.Sprintf(`{service_name=%q,k8s_namespace_name=%q}`, name, b.namespace())
	}
	q := url.Values{"query": {selector}, "start": {fmt.Sprint(start.UnixNano())}, "end": {fmt.Sprint(end.UnixNano())}, "limit": {fmt.Sprint(limit)}, "direction": {"BACKWARD"}}
	return b.get(ctx, b.Loki, "/loki/api/v1/query_range", q, false, 128<<10)
}

func (b Backend) Trace(ctx context.Context, id string) (json.RawMessage, error) {
	if b.Profile.Effective().Mode == "external" {
		return nil, errors.New("direct trace reads are unavailable for external profiles")
	}
	if len(id) < 16 || len(id) > 32 {
		return nil, errors.New("trace ID must be 16 to 32 hex characters")
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return nil, errors.New("trace ID must be hexadecimal")
		}
	}
	return b.get(ctx, b.Tempo, "/api/traces/"+strings.Repeat("0", 32-len(id))+id, nil, false, 256<<10)
}

func (b Backend) SearchTraces(ctx context.Context, name string, minutes int) (json.RawMessage, error) {
	return b.SearchTracesFiltered(ctx, name, "", minutes)
}

func (b Backend) SearchTracesFiltered(ctx context.Context, name, filter string, minutes int) (json.RawMessage, error) {
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	if minutes < 1 || minutes > 60 {
		return nil, errors.New("trace window must be 1 to 60 minutes")
	}
	now := time.Now()
	return b.SearchTracesWindowFiltered(ctx, name, filter, now.Add(-time.Duration(minutes)*time.Minute), now)
}

func (b Backend) SearchTracesWindow(ctx context.Context, name string, start, end time.Time) (json.RawMessage, error) {
	return b.SearchTracesWindowFiltered(ctx, name, "", start, end)
}

func (b Backend) SearchTracesWindowFiltered(ctx context.Context, name, filter string, start, end time.Time) (json.RawMessage, error) {
	if b.Profile.Effective().Mode == "external" {
		return nil, errors.New("trace search is unavailable for external profiles")
	}
	if !b.workload(name) {
		return nil, errors.New("unknown demo workload")
	}
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`{ resource.service.name = %q }`, name)
	switch filter {
	case "":
	case "error":
		query = fmt.Sprintf(`{ resource.service.name = %q && span.http.response.status_code >= 500 }`, name)
	case "slow_success":
		query = fmt.Sprintf(`{ resource.service.name = %q && span.http.response.status_code = 200 && duration >= 900ms }`, name)
	default:
		return nil, errors.New("unknown trace filter")
	}
	q := url.Values{"q": {query}, "start": {fmt.Sprint(start.Unix())}, "end": {fmt.Sprint(end.Unix())}, "limit": {"10"}}
	return b.get(ctx, b.Tempo, "/api/search", q, false, 64<<10)
}
