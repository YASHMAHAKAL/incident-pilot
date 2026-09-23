package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"incidentpilot/internal/incident"
	"incidentpilot/internal/onboarding"
)

func TestExternalCollectionPersistsOnlyScopedGenericEvidence(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	profile, err := onboarding.Parse(`{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":     observation("kubernetes/deployment", `{"spec":{}}`, now),
		"kubernetes_get_service":        observation("kubernetes/service", `{"spec":{}}`, now),
		"kubernetes_get_endpointslices": observation("kubernetes/endpointslices", `{"items":[]}`, now),
		"kubernetes_get_pods":           observation("kubernetes/pods", `{"items":[]}`, now),
		"kubernetes_get_events":         observation("kubernetes/events", `{"items":[]}`, now),
		"loki_get_demo_logs":            observation("loki", `{"result":[]}`, now),
	}}
	store := &fakeEvidenceStore{}
	collector := Collector{Caller: caller, Store: store, Profile: profile, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "payments", Service: "checkout", AlertName: "CheckoutErrors", StartedAt: now.Add(-time.Minute)}
	report, err := collector.Collect(context.Background(), inc)
	if err != nil || len(report.Evidence) != 6 || len(store.saved) != 6 {
		t.Fatalf("unexpected external evidence: %+v err=%v", report, err)
	}
	for _, record := range store.saved {
		if record.ResourceRef != "payments/checkout" {
			t.Fatalf("wrong evidence reference: %s", record.ResourceRef)
		}
		if strings.Contains(record.Tool, "prometheus") || strings.Contains(record.Tool, "orders_config") || strings.HasPrefix(record.Tool, "argocd_") || strings.HasPrefix(record.Tool, "tempo_") {
			t.Fatalf("demo-only tool used: %s", record.Tool)
		}
	}
	inc.Namespace = "default"
	if _, err := collector.Collect(context.Background(), inc); !errors.Is(err, ErrUnsupportedIncident) {
		t.Fatalf("out-of-scope incident accepted: %v", err)
	}
}

type fakeCaller struct {
	calls   []string
	results map[string]ToolObservation
	err     map[string]error
}

func (c *fakeCaller) Call(_ context.Context, tool string, _ any) (ToolObservation, error) {
	c.calls = append(c.calls, tool)
	if err := c.err[tool]; err != nil {
		return ToolObservation{}, err
	}
	return c.results[tool], nil
}

type fakeEvidenceStore struct {
	saved []Record
	err   error
}

func (s *fakeEvidenceStore) SaveBatch(_ context.Context, records []Record) error {
	s.saved = append(s.saved, records...)
	return s.err
}
func (*fakeEvidenceStore) ListByIncident(context.Context, string, int) ([]Record, error) {
	return nil, nil
}

func observation(source, data string, now time.Time) ToolObservation {
	return ToolObservation{Source: source, CollectedAt: now, Data: json.RawMessage(data)}
}

func TestCollectsPersistsAndNormalizesEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{"spec":{"replicas":1},"status":{"availableReplicas":0}}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{"spec":{"selector":{"app":"frontend"}}}`, now),
		"kubernetes_get_endpointslices":    observation("kubernetes/endpointslices", `{"items":[{"endpoints":[{"addresses":["10.0.0.1"]}]}]}`, now),
		"kubernetes_get_pods":              observation("kubernetes/pods", `{"items":[]}`, now),
		"kubernetes_get_events":            observation("kubernetes/events", `{"items":[{}]}`, now),
		"prometheus_get_demo_metric_range": observation("prometheus/range", `{"data":{"result":[{"values":[[1,"0.2"]]}]}}`, now),
		"loki_get_demo_logs":               observation("loki", `{"data":{"result":[{"values":[[1,"line"]]}]}}`, now),
		"tempo_search_demo_traces":         observation("tempo/search", `{"traces":[{"traceID":"1234567890abcdef"}]}`, now),
		"tempo_get_trace":                  observation("tempo", `{"batches":[{}]}`, now),
		"argocd_get_application":           observation("argocd/application", `{"application":"incidentpilot-demo","sync_status":"Synced","health_status":"Healthy","current_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target_revision":"main"}`, now),
		"argocd_get_revision_history":      observation("argocd/revision_history", `{"application":"incidentpilot-demo","deployments":[{"id":1,"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deployed_at":"2026-09-20T09:58:00Z"}]}`, now),
		"github_get_commit":                observation("github/commit", `{"repository":"example/incident-pilot","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","message":"reduce memory limit","committed_at":"2026-09-20T09:55:00Z"}`, now),
		"github_get_diff":                  observation("github/diff", `{"repository":"example/incident-pilot","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","files":[{"filename":"deploy/kind/30-demo.yaml","status":"modified","changes":2,"patch":"- 128Mi\n+ 48Mi"}]}`, now),
	}, err: map[string]error{}}
	store := &fakeEvidenceStore{}
	collector := Collector{Caller: caller, Store: store, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "incidentpilot-demo", Service: "frontend", StartedAt: now.Add(-5 * time.Minute)}
	report, err := collector.Collect(context.Background(), inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Evidence) != 13 || len(store.saved) != 13 {
		t.Fatalf("got %d evidence and %d saved", len(report.Evidence), len(store.saved))
	}
	if report.WindowStart != now.Add(-7*time.Minute) || report.WindowEnd != now {
		t.Fatalf("unexpected window %+v", report)
	}
	for _, record := range store.saved {
		if record.ID == "" || record.CollectionID != report.CollectionID || record.IncidentID != inc.ID || record.Source == "" || record.Summary == "" || !json.Valid(record.Payload) {
			t.Fatalf("invalid persisted record: %+v", record)
		}
		if record.Tool == "kubernetes_get_deployment" && record.WindowStart != nil {
			t.Fatal("point-in-time Kubernetes evidence got a telemetry window")
		}
		if strings.Contains(record.Tool, "prometheus") || record.Tool == "loki_get_demo_logs" || strings.HasPrefix(record.Tool, "tempo_") || record.Tool == "argocd_get_revision_history" || strings.HasPrefix(record.Tool, "github_") {
			if record.WindowStart == nil || record.WindowEnd == nil {
				t.Fatalf("missing time window: %s", record.Tool)
			}
		}
	}
	if got := caller.calls[len(caller.calls)-2:]; got[0] != "github_get_commit" || got[1] != "github_get_diff" {
		t.Fatalf("unexpected change call chain: %v", caller.calls)
	}
	if got := PayloadDigest(store.saved[0].Payload); len(got) != 64 {
		t.Fatalf("unexpected digest %q", got)
	}
}

func TestCollectionPartialFailureAndProvenanceRejection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("wrong/source", `{}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{"spec":{}}`, now),
		"kubernetes_get_endpointslices":    observation("kubernetes/endpointslices", `{"items":[]}`, now),
		"kubernetes_get_pods":              observation("kubernetes/pods", `{"items":[]}`, now),
		"kubernetes_get_events":            observation("kubernetes/events", `{"items":[]}`, now),
		"prometheus_get_demo_metric_range": observation("prometheus/range", `{"data":{"result":[]}}`, now),
		"loki_get_demo_logs":               observation("loki", `{"data":{"result":[]}}`, now),
		"tempo_search_demo_traces":         observation("tempo/search", `{"traces":[]}`, now),
	}, err: map[string]error{"kubernetes_get_pods": errors.New("unavailable")}}
	store := &fakeEvidenceStore{}
	collector := Collector{Caller: caller, Store: store, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "incidentpilot-demo", Service: "frontend", StartedAt: now.Add(-5 * time.Minute)}
	report, err := collector.Collect(context.Background(), inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 3 || len(store.saved) != 6 {
		t.Fatalf("failures=%+v saved=%d", report.Failures, len(store.saved))
	}
}

func TestWindowRejectsStaleAndUnsupportedIncidents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	if _, _, err := Window(incident.Incident{StartedAt: now.Add(-25 * time.Hour)}, now); !errors.Is(err, ErrWindowUnavailable) {
		t.Fatalf("got %v", err)
	}
	collector := Collector{Caller: &fakeCaller{}, Store: &fakeEvidenceStore{}, Now: func() time.Time { return now }}
	_, err := collector.Collect(context.Background(), incident.Incident{Namespace: "default", Service: "frontend", StartedAt: now.Add(-time.Minute)})
	if !errors.Is(err, ErrUnsupportedIncident) {
		t.Fatalf("got %v", err)
	}
}

func TestLatencyAlertSelectsSuccessfulLatencyMetric(t *testing.T) {
	inc := incident.Incident{AlertName: "DemoCheckoutLatency"}
	if got := initialMetric(inc); got != "success_latency_avg" {
		t.Fatalf("latency alert selected %q", got)
	}
	inc.AlertName = "DemoCheckoutErrors"
	if got := initialMetric(inc); got != "error_rate" {
		t.Fatalf("error alert selected %q", got)
	}
	if got := incidentTraceFilter(incident.Incident{AlertName: "DemoCheckoutLatency"}); got != "slow_success" {
		t.Fatalf("latency alert selected trace filter %q", got)
	}
	if got := incidentTraceFilter(incident.Incident{AlertName: "DemoCheckoutErrors"}); got != "error" {
		t.Fatalf("error alert selected trace filter %q", got)
	}
}

func TestTargetedCollectionScopesWorkloadAndMetric(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{"spec":{}}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{"spec":{}}`, now),
		"kubernetes_get_endpointslices":    observation("kubernetes/endpointslices", `{"items":[]}`, now),
		"kubernetes_get_pods":              observation("kubernetes/pods", `{"items":[]}`, now),
		"kubernetes_get_events":            observation("kubernetes/events", `{"items":[]}`, now),
		"prometheus_get_demo_metric_range": observation("prometheus/range", `{"data":{"result":[]}}`, now),
		"loki_get_demo_logs":               observation("loki", `{"data":{"result":[]}}`, now),
		"tempo_search_demo_traces":         observation("tempo/search", `{"traces":[]}`, now),
	}}
	store := &fakeEvidenceStore{}
	collector := Collector{Caller: caller, Store: store, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "incidentpilot-demo", Service: "frontend", StartedAt: now.Add(-time.Minute)}
	if _, err := collector.CollectTarget(context.Background(), inc, "kube-system", "heap_bytes"); !errors.Is(err, ErrUnsupportedIncident) {
		t.Fatalf("unsafe target accepted: %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatal("unsafe target called MCP")
	}
	report, err := collector.CollectTarget(context.Background(), inc, "payments-api", "heap_bytes")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range report.Evidence {
		if record.ResourceRef != "incidentpilot-demo/payments-api" {
			t.Fatalf("wrong target reference: %+v", record)
		}
		if record.Tool == "prometheus_get_demo_metric_range" && !strings.Contains(string(record.Parameters), `"metric":"heap_bytes"`) {
			t.Fatalf("wrong metric: %s", record.Parameters)
		}
	}
	for _, call := range caller.calls {
		if strings.HasPrefix(call, "argocd_") || strings.HasPrefix(call, "github_") {
			t.Fatalf("targeted workload collection unexpectedly read changes: %v", caller.calls)
		}
	}
}

func TestOrdersCollectionReadsAllowlistedConfigAndObservedCrashLoopLogs(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{"diagnosticConfigReferences":[{"container":"orders-api","environmentVariable":"DEMO_ORDER_MODE","configMap":"orders-config","key":"order_mode"}]}`, now),
		"kubernetes_get_orders_config":     observation("kubernetes/configmap", `{"metadata":{"name":"orders-config"},"data":{"order_mode":"unsupported"}}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{"spec":{}}`, now),
		"kubernetes_get_endpointslices":    observation("kubernetes/endpointslices", `{"items":[]}`, now),
		"kubernetes_get_pods":              observation("kubernetes/pods", `{"items":[{"metadata":{"name":"orders-api-abc-123"},"status":{"containerStatuses":[{"name":"orders-api","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}]}`, now),
		"kubernetes_get_pod_logs":          {Source: "kubernetes/pod_logs", CollectedAt: now, Text: `{"msg":"demo stopped","error":"invalid order mode \"unsupported\""}`},
		"kubernetes_get_events":            observation("kubernetes/events", `{"items":[]}`, now),
		"prometheus_get_demo_metric_range": observation("prometheus/range", `{"data":{"result":[]}}`, now),
		"loki_get_demo_logs":               observation("loki", `{"data":{"result":[]}}`, now),
		"tempo_search_demo_traces":         observation("tempo/search", `{"traces":[]}`, now),
	}}
	store := &fakeEvidenceStore{}
	collector := Collector{Caller: caller, Store: store, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "incidentpilot-demo", Service: "orders-api", StartedAt: now.Add(-time.Minute)}
	report, err := collector.CollectTarget(context.Background(), inc, "orders-api", "error_rate")
	if err != nil {
		t.Fatal(err)
	}
	foundConfig, foundLogs := false, false
	for _, record := range report.Evidence {
		switch record.Tool {
		case "kubernetes_get_orders_config":
			foundConfig = record.ResourceRef == "incidentpilot-demo/configmap/orders-config"
		case "kubernetes_get_pod_logs":
			foundLogs = record.ResourceRef == "incidentpilot-demo/orders-api-abc-123" && strings.Contains(string(record.Payload), "invalid order mode")
		}
	}
	if !foundConfig || !foundLogs {
		t.Fatalf("missing bounded config or pod-log evidence: %+v", report.Evidence)
	}
}

func TestCrashLoopPodAcceptsRestartingErrorBetweenBackoffStates(t *testing.T) {
	pod := crashLoopPod(json.RawMessage(`{"items":[{"metadata":{"name":"orders-api-7f6d8c9b5-x2abc"},"status":{"containerStatuses":[{"name":"orders-api","restartCount":2,"state":{"terminated":{"reason":"Error"}}}]}}]}`), "orders-api", "orders-api")
	if pod != "orders-api-7f6d8c9b5-x2abc" {
		t.Fatalf("terminated restart state was not recognized: %q", pod)
	}
}

func TestCrashLoopPodUsesConfiguredContainer(t *testing.T) {
	payload := json.RawMessage(`{"items":[{"metadata":{"name":"checkout-abc"},"status":{"containerStatuses":[{"name":"web","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}]}`)
	if got := crashLoopPod(payload, "checkout", "web"); got != "checkout-abc" {
		t.Fatalf("configured container crash loop not selected: %q", got)
	}
	if got := crashLoopPod(payload, "checkout", "sidecar"); got != "" {
		t.Fatalf("unconfigured container selected: %q", got)
	}
}

func TestChangeCollectionDoesNotTrustRevisionOutsideArgoHistory(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{}`, now),
		"kubernetes_get_endpointslices":    observation("kubernetes/endpointslices", `{"items":[]}`, now),
		"kubernetes_get_pods":              observation("kubernetes/pods", `{"items":[]}`, now),
		"kubernetes_get_events":            observation("kubernetes/events", `{"items":[]}`, now),
		"prometheus_get_demo_metric_range": observation("prometheus/range", `{"data":{"result":[]}}`, now),
		"loki_get_demo_logs":               observation("loki", `{"data":{"result":[]}}`, now),
		"tempo_search_demo_traces":         observation("tempo/search", `{"traces":[]}`, now),
		"argocd_get_application":           observation("argocd/application", `{"current_revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, now),
		"argocd_get_revision_history":      observation("argocd/revision_history", `{"deployments":[]}`, now),
	}}
	collector := Collector{Caller: caller, Store: &fakeEvidenceStore{}, Now: func() time.Time { return now }}
	inc := incident.Incident{ID: "00000000-0000-0000-0000-000000000001", Namespace: "incidentpilot-demo", Service: "frontend", StartedAt: now.Add(-time.Minute)}
	if _, err := collector.Collect(context.Background(), inc); err != nil {
		t.Fatal(err)
	}
	for _, call := range caller.calls {
		if strings.HasPrefix(call, "github_") {
			t.Fatalf("GitHub was queried without a correlated Argo deployment: %v", caller.calls)
		}
	}
}

func TestRedactsKnownCredentialFieldsFromPersistedPayload(t *testing.T) {
	t.Parallel()
	clean, err := redactPayload(json.RawMessage(`{"password":"value","nested":{"token":"value"},"log":"Authorization: Bearer-value password=another"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clean), "value") || strings.Contains(string(clean), "another") {
		t.Fatalf("credential leaked: %s", clean)
	}
	if !strings.Contains(string(clean), "redacted") {
		t.Fatalf("redaction missing: %s", clean)
	}
}
