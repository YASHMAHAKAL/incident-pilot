package remediation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"incidentpilot/internal/agent"
	"incidentpilot/internal/evidence"
	"incidentpilot/internal/policy"
	"incidentpilot/internal/remediation"
)

const (
	incidentID      = "00000000-0000-0000-0000-000000000001"
	investigationID = "00000000-0000-0000-0000-000000000002"
	evidenceID      = "00000000-0000-0000-0000-000000000003"
)

type source struct {
	report  agent.Report
	records []evidence.Record
}

func (s source) GetInvestigation(context.Context, string) (agent.Report, error) { return s.report, nil }
func (s source) EvidenceForRemediation(context.Context, string, []string) ([]evidence.Record, error) {
	return s.records, nil
}

type store struct {
	result remediation.Result
	input  remediation.PolicyInput
	saves  int
}

func (s *store) SaveRemediationEvaluation(_ context.Context, result remediation.Result, input remediation.PolicyInput) error {
	s.result, s.input, s.saves = result, input, s.saves+1
	return nil
}
func (s *store) GetRemediation(context.Context, string) (remediation.Result, error) {
	return s.result, nil
}

type failingEvaluator struct{}

func (failingEvaluator) Evaluate(context.Context, remediation.PolicyInput) (remediation.PolicyDecision, error) {
	return remediation.PolicyDecision{}, errors.New("policy unavailable")
}

func safeRequest() remediation.Request {
	return remediation.Request{
		IncidentID: incidentID, InvestigationID: investigationID, Operation: "update_resource_limit",
		Target: remediation.Target{Repository: "owner/repository", Path: "deploy/kind/30-demo.yaml", Namespace: "incidentpilot-demo", Kind: "Deployment", Name: "payments-api", Container: "payments-api"},
		Change: remediation.Change{Field: "memory_limit", Before: "48Mi", After: "128Mi"},
		Reason: "Verified OOM requires a bounded memory limit increase.", EvidenceIDs: []string{evidenceID},
	}
}

func service(t *testing.T, targetStore *store) remediation.Service {
	t.Helper()
	evaluator, err := policy.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return remediation.Service{
		Source: source{
			report:  agent.Report{ID: investigationID, IncidentID: incidentID, Status: agent.StatusRootCauseFound, RootCause: &agent.RootCause{Cause: "memory_limit_oom", EvidenceIDs: []string{evidenceID}}},
			records: []evidence.Record{{ID: evidenceID, IncidentID: incidentID, Tool: "kubernetes_get_deployment", Source: "kubernetes/deployment", ResourceRef: "incidentpilot-demo/payments-api", Payload: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"payments-api","resources":{"limits":{"memory":"48Mi"}}}]}}}}`)}},
		},
		Store: targetStore, Evaluator: evaluator,
		Config: remediation.Config{Environment: "development", Repository: "owner/repository", AllowedPath: "deploy/kind/30-demo.yaml", Now: func() time.Time { return time.Date(2026, 9, 21, 1, 2, 3, 0, time.UTC) }},
	}
}

func TestRequestAllowsAndAuditsSafeProposal(t *testing.T) {
	targetStore := &store{}
	result, err := service(t, targetStore).Request(context.Background(), safeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Allowed || result.Proposal.Status != remediation.StatusPolicyAllowed || result.Audit.Outcome != "allowed" || targetStore.saves != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !targetStore.input.RepositoryAllowed || !targetStore.input.PathAllowed || targetStore.input.RootCause != "memory_limit_oom" || !targetStore.input.ResourceChangeSafe {
		t.Fatalf("trusted policy input was not derived correctly: %+v", targetStore.input)
	}
}

func TestRequestPersistsPolicyDenials(t *testing.T) {
	tests := []struct {
		name   string
		change func(*remediation.Request)
		reason string
	}{
		{"repository", func(r *remediation.Request) { r.Target.Repository = "other/repository" }, "repository_not_allowed"},
		{"path", func(r *remediation.Request) { r.Target.Path = "other/file.yaml" }, "path_not_allowed"},
		{"namespace", func(r *remediation.Request) { r.Target.Namespace = "kube-system" }, "protected_namespace"},
		{"secret", func(r *remediation.Request) { r.Target.Kind = "Secret" }, "secret_target"},
		{"direct mutation", func(r *remediation.Request) { r.Operation = "direct_cluster_patch" }, "direct_cluster_mutation"},
		{"delete", func(r *remediation.Request) { r.Operation = "delete_resource" }, "destructive_operation"},
		{"unsupported operation", func(r *remediation.Request) { r.Operation = "change_image" }, "unsupported_operation"},
		{"unverified before", func(r *remediation.Request) { r.Change.Before = "32Mi" }, "unsafe_resource_change"},
		{"unsafe increase", func(r *remediation.Request) { r.Change.After = "1024Mi" }, "unsafe_resource_change"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetStore := &store{}
			request := safeRequest()
			test.change(&request)
			result, err := service(t, targetStore).Request(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision.Allowed || !contains(result.Decision.Reasons, test.reason) || result.Proposal.Status != remediation.StatusPolicyDenied || targetStore.saves != 1 {
				t.Fatalf("unexpected denial: %+v", result)
			}
		})
	}
}

func TestRequestRejectsUnverifiedEvidenceAndPolicyBypass(t *testing.T) {
	request := safeRequest()
	request.EvidenceIDs = []string{"00000000-0000-0000-0000-000000000004"}
	targetStore := &store{}
	_, err := service(t, targetStore).Request(context.Background(), request)
	if !errors.Is(err, remediation.ErrInvalidProposal) || targetStore.saves != 0 {
		t.Fatalf("unverified evidence was not rejected: %v", err)
	}

	svc := service(t, targetStore)
	svc.Evaluator = failingEvaluator{}
	_, err = svc.Request(context.Background(), safeRequest())
	if err == nil || targetStore.saves != 0 {
		t.Fatalf("policy failure was bypassed: %v", err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
