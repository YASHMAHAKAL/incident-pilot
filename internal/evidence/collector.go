package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"incidentpilot/internal/incident"
)

type planItem struct {
	tool, source, resourceRef string
	arguments                 map[string]any
	timed                     bool
}

func (c Collector) Collect(ctx context.Context, inc incident.Incident) (Report, error) {
	return c.collect(ctx, inc, inc.Service, "error_rate", true)
}

// CollectTarget gathers the same bounded evidence for a validated downstream
// demo workload. It is the only targeted collection operation the agent uses.
func (c Collector) CollectTarget(ctx context.Context, inc incident.Incident, workload, metric string) (Report, error) {
	if metric != "error_rate" && metric != "heap_bytes" {
		return Report{}, ErrUnsupportedIncident
	}
	return c.collect(ctx, inc, workload, metric, false)
}

func (c Collector) collect(ctx context.Context, inc incident.Incident, workload, metric string, includeChanges bool) (Report, error) {
	if c.Caller == nil || c.Store == nil {
		return Report{}, errors.New("evidence collection is not configured")
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	start, end, err := Window(inc, now)
	if err != nil {
		return Report{}, err
	}
	if inc.Namespace != "incidentpilot-demo" || !allowedWorkload(inc.Service) || !allowedWorkload(workload) {
		return Report{}, ErrUnsupportedIncident
	}
	ctx, span := otel.Tracer("incidentpilot/evidence").Start(ctx, "evidence.collect")
	defer span.End()
	span.SetAttributes(attribute.String("incident.id", inc.ID))
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	collectionID := uuid.NewString()
	report := Report{CollectionID: collectionID, IncidentID: inc.ID, WindowStart: start, WindowEnd: end, Evidence: []Record{}, Failures: []Failure{}}
	startValue, endValue := start.Format(time.RFC3339), end.Format(time.RFC3339)
	plan := []planItem{
		{tool: "kubernetes_get_deployment", source: "kubernetes/deployment", arguments: map[string]any{"workload": workload}},
		{tool: "kubernetes_get_service", source: "kubernetes/service", arguments: map[string]any{"workload": workload}},
		{tool: "kubernetes_get_pods", source: "kubernetes/pods", arguments: map[string]any{"workload": workload}},
		{tool: "kubernetes_get_events", source: "kubernetes/events", arguments: map[string]any{"workload": workload}},
		{tool: "prometheus_get_demo_metric_range", source: "prometheus/range", arguments: map[string]any{"workload": workload, "metric": metric, "start": startValue, "end": endValue}, timed: true},
		{tool: "loki_get_demo_logs", source: "loki", arguments: map[string]any{"workload": workload, "start": startValue, "end": endValue, "limit": 20}, timed: true},
		{tool: "tempo_search_demo_traces", source: "tempo/search", arguments: map[string]any{"workload": workload, "start": startValue, "end": endValue}, timed: true},
	}
	for _, item := range plan {
		observation, callErr := c.Caller.Call(ctx, item.tool, item.arguments)
		if callErr != nil {
			report.Failures = append(report.Failures, Failure{Tool: item.tool, Reason: "tool call failed"})
			continue
		}
		record, recordErr := makeRecord(inc, collectionID, workload, item, observation, start, end)
		if recordErr != nil {
			report.Failures = append(report.Failures, Failure{Tool: item.tool, Reason: "invalid tool result"})
			continue
		}
		report.Evidence = append(report.Evidence, record)
		if item.tool == "tempo_search_demo_traces" {
			if traceID := firstTraceID(record.Payload); traceID != "" {
				traceItem := planItem{tool: "tempo_get_trace", source: "tempo", arguments: map[string]any{"trace_id": traceID}, timed: true}
				traceObservation, traceErr := c.Caller.Call(ctx, traceItem.tool, traceItem.arguments)
				if traceErr != nil {
					report.Failures = append(report.Failures, Failure{Tool: traceItem.tool, Reason: "tool call failed"})
				} else if traceRecord, err := makeRecord(inc, collectionID, workload, traceItem, traceObservation, start, end); err != nil {
					report.Failures = append(report.Failures, Failure{Tool: traceItem.tool, Reason: "invalid tool result"})
				} else {
					report.Evidence = append(report.Evidence, traceRecord)
				}
			}
		}
	}
	if includeChanges {
		c.collectChanges(ctx, inc, collectionID, start, end, &report)
	}
	if len(report.Evidence) == 0 {
		return report, errors.New("no evidence could be collected")
	}
	if err := c.Store.SaveBatch(ctx, report.Evidence); err != nil {
		return Report{}, fmt.Errorf("persist evidence: %w", err)
	}
	span.SetAttributes(attribute.Int("evidence.count", len(report.Evidence)), attribute.Int("evidence.failures", len(report.Failures)))
	return report, nil
}

func allowedWorkload(name string) bool {
	return name == "frontend" || name == "orders-api" || name == "payments-api"
}

func makeRecord(inc incident.Incident, collectionID, workload string, item planItem, obs ToolObservation, start, end time.Time) (Record, error) {
	if obs.Source != item.source || obs.CollectedAt.IsZero() || len(obs.Data) == 0 || !json.Valid(obs.Data) {
		return Record{}, errors.New("missing or mismatched tool provenance")
	}
	payload, err := redactPayload(compactPayload(item.tool, obs.Data))
	if err != nil {
		return Record{}, err
	}
	if len(payload) > 256<<10 {
		return Record{}, errors.New("evidence payload too large")
	}
	params, err := json.Marshal(item.arguments)
	if err != nil {
		return Record{}, err
	}
	if len(params) > 2048 {
		return Record{}, errors.New("parameters too large")
	}
	ref := item.resourceRef
	if ref == "" {
		ref = inc.Namespace + "/" + workload
	}
	ref = evidenceResourceRef(item.tool, payload, ref)
	record := Record{ID: uuid.NewString(), CollectionID: collectionID, IncidentID: inc.ID, Tool: item.tool, Source: item.source, CollectedAt: obs.CollectedAt.UTC(), Parameters: params, Summary: summarize(item.tool, payload, end), ResourceRef: ref, Payload: payload}
	if item.timed {
		record.WindowStart = &start
		record.WindowEnd = &end
	}
	if item.tool == "tempo_get_trace" {
		record.TraceID, _ = item.arguments["trace_id"].(string)
	}
	return record, nil
}

func evidenceResourceRef(tool string, payload json.RawMessage, fallback string) string {
	var identity struct {
		Application string `json:"application"`
		Repository  string `json:"repository"`
	}
	if json.Unmarshal(payload, &identity) != nil {
		return fallback
	}
	if strings.HasPrefix(tool, "argocd_") && identity.Application != "" {
		return "argocd/" + identity.Application
	}
	if strings.HasPrefix(tool, "github_") && identity.Repository != "" {
		return "github/" + identity.Repository
	}
	return fallback
}

// collectChanges follows a trusted chain: fixed Argo CD application -> a
// revision deployed near the incident -> the same revision in a fixed GitHub
// repository. Model output can neither choose the repository nor the revision.
func (c Collector) collectChanges(ctx context.Context, inc incident.Incident, collectionID string, start, end time.Time, report *Report) {
	startValue, endValue := start.Format(time.RFC3339), end.Format(time.RFC3339)
	application := planItem{tool: "argocd_get_application", source: "argocd/application", resourceRef: "argocd/incidentpilot-demo", arguments: map[string]any{}}
	if !c.collectChangeItem(ctx, inc, collectionID, "", application, start, end, report) {
		return
	}
	history := planItem{tool: "argocd_get_revision_history", source: "argocd/revision_history", resourceRef: "argocd/incidentpilot-demo", arguments: map[string]any{"start": startValue, "end": endValue}, timed: true}
	observation, err := c.Caller.Call(ctx, history.tool, history.arguments)
	if err != nil {
		report.Failures = append(report.Failures, Failure{Tool: history.tool, Reason: "tool call failed"})
		return
	}
	record, err := makeRecord(inc, collectionID, "", history, observation, start, end)
	if err != nil {
		report.Failures = append(report.Failures, Failure{Tool: history.tool, Reason: "invalid tool result"})
		return
	}
	report.Evidence = append(report.Evidence, record)
	revision := newestRevision(record.Payload)
	if revision == "" {
		return
	}
	for _, item := range []planItem{
		{tool: "github_get_commit", source: "github/commit", resourceRef: "github/configured-repository", arguments: map[string]any{"revision": revision, "start": startValue, "end": endValue}, timed: true},
		{tool: "github_get_diff", source: "github/diff", resourceRef: "github/configured-repository", arguments: map[string]any{"revision": revision, "start": startValue, "end": endValue}, timed: true},
	} {
		c.collectChangeItem(ctx, inc, collectionID, "", item, start, end, report)
	}
}

func (c Collector) collectChangeItem(ctx context.Context, inc incident.Incident, collectionID, workload string, item planItem, start, end time.Time, report *Report) bool {
	observation, err := c.Caller.Call(ctx, item.tool, item.arguments)
	if err != nil {
		report.Failures = append(report.Failures, Failure{Tool: item.tool, Reason: "tool call failed"})
		return false
	}
	record, err := makeRecord(inc, collectionID, workload, item, observation, start, end)
	if err != nil {
		report.Failures = append(report.Failures, Failure{Tool: item.tool, Reason: "invalid tool result"})
		return false
	}
	report.Evidence = append(report.Evidence, record)
	return true
}

func newestRevision(raw json.RawMessage) string {
	var history struct {
		Deployments []struct {
			Revision string `json:"revision"`
		} `json:"deployments"`
	}
	if json.Unmarshal(raw, &history) != nil || len(history.Deployments) == 0 {
		return ""
	}
	revision := history.Deployments[0].Revision
	if len(revision) < 7 || len(revision) > 64 {
		return ""
	}
	for _, char := range revision {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return ""
		}
	}
	return revision
}

func compactPayload(tool string, raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	if tool == "tempo_search_demo_traces" {
		if traces, ok := obj["traces"]; ok {
			return json.RawMessage(`{"traces":` + string(traces) + `}`)
		}
	}
	if tool == "loki_get_demo_logs" || tool == "prometheus_get_demo_metric_range" {
		var wrapper struct {
			Data struct {
				Result json.RawMessage `json:"result"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Data.Result) > 0 {
			return json.RawMessage(`{"result":` + string(wrapper.Data.Result) + `}`)
		}
	}
	return raw
}

var sensitiveText = regexp.MustCompile(`(?i)\b(password|token|authorization|secret)\b\s*[:=]\s*[^\s,;"}]+`)

// Telemetry is untrusted and may accidentally contain a credential. Retain the
// diagnostic shape while removing common credential fields and inline values.
func redactPayload(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	clean := redactValue(value)
	return json.Marshal(clean)
}

func redactValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, child := range item {
			switch strings.ToLower(key) {
			case "password", "token", "authorization", "secret", "secretkeyref", "stringdata":
				out[key] = "<redacted>"
				continue
			}
			out[key] = redactValue(child)
		}
		return out
	case []any:
		for index := range item {
			item[index] = redactValue(item[index])
		}
		return item
	case string:
		return sensitiveText.ReplaceAllString(item, "$1=<redacted>")
	}
	return value
}

func firstTraceID(raw json.RawMessage) string {
	var result struct {
		Traces []struct {
			TraceID string `json:"traceID"`
		} `json:"traces"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Traces) == 0 {
		return ""
	}
	id := result.Traces[0].TraceID
	if len(id) < 16 || len(id) > 32 {
		return ""
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	return id
}

func summarize(tool string, raw json.RawMessage, incidentEnd time.Time) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return "Structured result could not be summarized"
	}
	switch tool {
	case "kubernetes_get_deployment":
		return fmt.Sprintf("Deployment replicas: desired %s, available %s", number(nested(obj, "spec", "replicas")), number(nested(obj, "status", "availableReplicas")))
	case "kubernetes_get_service":
		selector, _ := nested(obj, "spec", "selector").(map[string]any)
		return fmt.Sprintf("Service selector has %d labels", len(selector))
	case "kubernetes_get_pods":
		items, _ := obj["items"].([]any)
		return fmt.Sprintf("%d workload pods returned", len(items))
	case "kubernetes_get_events":
		items, _ := obj["items"].([]any)
		return fmt.Sprintf("%d workload events returned", len(items))
	case "prometheus_get_demo_metric_range":
		items, _ := obj["result"].([]any)
		points := 0
		for _, item := range items {
			m, _ := item.(map[string]any)
			values, _ := m["values"].([]any)
			points += len(values)
		}
		return fmt.Sprintf("Error-rate series: %d, samples: %d", len(items), points)
	case "loki_get_demo_logs":
		items, _ := obj["result"].([]any)
		entries := 0
		for _, item := range items {
			m, _ := item.(map[string]any)
			values, _ := m["values"].([]any)
			entries += len(values)
		}
		return fmt.Sprintf("Log streams: %d, entries: %d", len(items), entries)
	case "tempo_search_demo_traces":
		items, _ := obj["traces"].([]any)
		return fmt.Sprintf("Traces found: %d", len(items))
	case "tempo_get_trace":
		items, _ := obj["batches"].([]any)
		return fmt.Sprintf("Trace batches: %d", len(items))
	case "argocd_get_application":
		return fmt.Sprintf("Argo CD application sync=%s health=%s revision=%s", textValue(obj["sync_status"]), textValue(obj["health_status"]), shortRevision(textValue(obj["current_revision"])))
	case "argocd_get_revision_history":
		items, _ := obj["deployments"].([]any)
		if len(items) == 0 {
			return "No Argo CD deployments found near the incident window"
		}
		latest, _ := items[0].(map[string]any)
		deployed, _ := time.Parse(time.RFC3339, textValue(latest["deployed_at"]))
		age := incidentEnd.Sub(deployed).Round(time.Second)
		if age < 0 {
			age = 0
		}
		return fmt.Sprintf("Latest nearby Argo CD deployment revision %s was %s before the incident window ended", shortRevision(textValue(latest["revision"])), age)
	case "github_get_commit":
		return fmt.Sprintf("GitHub commit %s at %s", shortRevision(textValue(obj["sha"])), textValue(obj["committed_at"]))
	case "github_get_diff":
		items, _ := obj["files"].([]any)
		return fmt.Sprintf("GitHub commit %s changed %d files", shortRevision(textValue(obj["sha"])), len(items))
	}
	return "Structured observation collected"
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func nested(obj map[string]any, keys ...string) any {
	var current any = obj
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}
func number(v any) string {
	if v == nil {
		return "0"
	}
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return "unknown"
	}
}

// PayloadDigest can be used by future RCA reports to detect altered raw evidence.
func PayloadDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
