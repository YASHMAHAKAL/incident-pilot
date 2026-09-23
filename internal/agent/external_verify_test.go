package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"incidentpilot/internal/evidence"
	"incidentpilot/internal/incident"
	"incidentpilot/internal/onboarding"
)

func externalProfile(t *testing.T, verifier string) onboarding.Profile {
	t.Helper()
	profile, err := onboarding.Parse(fmt.Sprintf(`{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"web","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}],"verifiers":[%q]}`, verifier))
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestExternalOOMRequiresRecentScopedPodAndDeployment(t *testing.T) {
	now := time.Now().UTC()
	profile := externalProfile(t, "kubernetes_oom")
	inc := incident.Incident{ID: testIncidentID, Namespace: "payments", Service: "checkout", AlertName: "CheckoutErrors", StartedAt: now.Add(-time.Minute)}
	deployment := evidence.Record{ID: deploymentEvidenceID, Tool: "kubernetes_get_deployment", Source: "kubernetes/deployment", ResourceRef: "payments/checkout", Payload: json.RawMessage(`{"metadata":{"name":"checkout"},"spec":{"template":{"spec":{"containers":[{"name":"web","resources":{"limits":{"memory":"256Mi"}}}]}}}}`)}
	podPayload := fmt.Sprintf(`{"items":[{"metadata":{"name":"checkout-abc","labels":{"app":"checkout"}},"spec":{"containers":[{"name":"web","resources":{"limits":{"memory":"256Mi"}}}]},"status":{"containerStatuses":[{"name":"web","lastState":{"terminated":{"reason":"OOMKilled","finishedAt":%q}}}]}}]}`, now.Format(time.RFC3339Nano))
	pods := evidence.Record{ID: podEvidenceID, Tool: "kubernetes_get_pods", Source: "kubernetes/pods", ResourceRef: "payments/checkout", Payload: json.RawMessage(podPayload)}
	records := []evidence.Record{deployment, pods}
	cause := externalVerifiedCause(profile, inc, records)
	if cause == nil || cause.Component != "checkout" || cause.Cause != causeMemoryLimitOOM || len(cause.EvidenceIDs) != 2 {
		t.Fatalf("external OOM not verified: %+v", cause)
	}
	pods.Payload = json.RawMessage(strings.Replace(podPayload, `"app":"checkout"`, `"app":"other"`, 1))
	if cause := externalVerifiedCause(profile, inc, []evidence.Record{deployment, pods}); cause != nil {
		t.Fatalf("accepted pod outside selector: %+v", cause)
	}
	pods.Payload = json.RawMessage(strings.Replace(podPayload, now.Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), 1))
	if cause := externalVerifiedCause(profile, inc, []evidence.Record{deployment, pods}); cause != nil {
		t.Fatalf("accepted stale OOM: %+v", cause)
	}
	pods.Payload = json.RawMessage(strings.Replace(podPayload, `"memory":"256Mi"`, `"memory":"512Mi"`, 1))
	if cause := externalVerifiedCause(profile, inc, []evidence.Record{deployment, pods}); cause != nil {
		t.Fatalf("accepted mismatched pod and Deployment limits: %+v", cause)
	}
}

func TestExternalImagePullRequiresMatchingRecentEvent(t *testing.T) {
	now := time.Now().UTC()
	profile := externalProfile(t, "image_pull")
	inc := incident.Incident{Namespace: "payments", Service: "checkout", AlertName: "CheckoutErrors", StartedAt: now.Add(-time.Minute)}
	image := "registry.example/checkout:v2"
	deployment := evidence.Record{ID: deploymentEvidenceID, Tool: "kubernetes_get_deployment", Source: "kubernetes/deployment", ResourceRef: "payments/checkout", Payload: json.RawMessage(fmt.Sprintf(`{"metadata":{"name":"checkout"},"spec":{"template":{"spec":{"containers":[{"name":"web","image":%q,"imagePullPolicy":"IfNotPresent"}]}}}}`, image))}
	pods := evidence.Record{ID: podEvidenceID, Tool: "kubernetes_get_pods", Source: "kubernetes/pods", ResourceRef: "payments/checkout", Payload: json.RawMessage(fmt.Sprintf(`{"items":[{"metadata":{"name":"checkout-abc","labels":{"app":"checkout"}},"spec":{"containers":[{"name":"web","image":%q}]},"status":{"containerStatuses":[{"name":"web","image":%q,"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}]}`, image, image))}
	message := "Failed to pull image " + image
	events := evidence.Record{ID: eventEvidenceID, Tool: "kubernetes_get_events", Source: "kubernetes/events", ResourceRef: "payments/checkout", Payload: json.RawMessage(fmt.Sprintf(`{"items":[{"reason":"Failed","message":%q,"metadata":{"creationTimestamp":%q},"involvedObject":{"kind":"Pod","name":"checkout-abc"}}]}`, message, now.Format(time.RFC3339Nano)))}
	cause := externalVerifiedCause(profile, inc, []evidence.Record{deployment, pods, events})
	if cause == nil || cause.Cause != causeUnpullableImage || len(cause.EvidenceIDs) != 3 {
		t.Fatalf("external image pull not verified: %+v", cause)
	}
	events.ResourceRef = "default/checkout"
	if cause := externalVerifiedCause(profile, inc, []evidence.Record{deployment, pods, events}); cause != nil {
		t.Fatalf("accepted event outside namespace: %+v", cause)
	}
}

func TestExternalAgentPersistsVerifiedRCAWithoutLLM(t *testing.T) {
	now := time.Now().UTC()
	profile := externalProfile(t, "kubernetes_oom")
	inc := incident.Incident{ID: testIncidentID, Namespace: "payments", Service: "checkout", AlertName: "CheckoutErrors", StartedAt: now.Add(-time.Minute)}
	records := []evidence.Record{
		{ID: deploymentEvidenceID, Tool: "kubernetes_get_deployment", Source: "kubernetes/deployment", ResourceRef: "payments/checkout", Payload: json.RawMessage(`{"metadata":{"name":"checkout"},"spec":{"template":{"spec":{"containers":[{"name":"web","resources":{"limits":{"memory":"256Mi"}}}]}}}}`)},
		{ID: podEvidenceID, Tool: "kubernetes_get_pods", Source: "kubernetes/pods", ResourceRef: "payments/checkout", Payload: json.RawMessage(fmt.Sprintf(`{"items":[{"metadata":{"name":"checkout-abc","labels":{"app":"checkout"}},"spec":{"containers":[{"name":"web","resources":{"limits":{"memory":"256Mi"}}}]},"status":{"containerStatuses":[{"name":"web","lastState":{"terminated":{"reason":"OOMKilled","finishedAt":%q}}}]}}]}`, now.Format(time.RFC3339Nano)))},
	}
	collector := &testCollector{initial: evidence.Report{Evidence: records}}
	reports := &testReports{}
	investigator := Investigator{Incidents: testIncidents{inc: inc}, Collector: collector, Reports: reports, Profile: profile}
	report, err := investigator.Run(context.Background(), testIncidentID)
	if err != nil || report.Status != StatusRootCauseFound || report.RootCause == nil || report.LLMCalls != 0 || reports.saved.ID != report.ID {
		t.Fatalf("external RCA not persisted: %+v err=%v", report, err)
	}
}
