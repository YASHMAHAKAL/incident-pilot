package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"incidentpilot/internal/incident"
)

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
	if len(report.Evidence) != 12 || len(store.saved) != 12 {
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
	if len(report.Failures) != 3 || len(store.saved) != 5 {
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

func TestTargetedCollectionScopesWorkloadAndMetric(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{"spec":{}}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{"spec":{}}`, now),
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

func TestChangeCollectionDoesNotTrustRevisionOutsideArgoHistory(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	caller := &fakeCaller{results: map[string]ToolObservation{
		"kubernetes_get_deployment":        observation("kubernetes/deployment", `{}`, now),
		"kubernetes_get_service":           observation("kubernetes/service", `{}`, now),
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
