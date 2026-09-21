package policy

import (
	"context"
	"testing"

	"incidentpilot/internal/remediation"
)

func safeInput() remediation.PolicyInput {
	return remediation.PolicyInput{
		Environment: "development", Repository: "owner/repository", RepositoryAllowed: true,
		Path: "deploy/kind/30-demo.yaml", PathAllowed: true, Operation: "update_resource_limit",
		Namespace: "incidentpilot-demo", TargetKind: "Deployment", TargetName: "payments-api",
		Container: "payments-api", Field: "memory_limit", BeforeMiB: 48, AfterMiB: 128, ObservedBeforeMiB: 48,
		EvidenceVerified: true, RootCause: "memory_limit_oom", ResourceChangeSafe: true,
	}
}

func TestEvaluatorAllowsOnlySafeProposal(t *testing.T) {
	evaluator, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluator.Evaluate(context.Background(), safeInput())
	if err != nil || !decision.Allowed || len(decision.Reasons) != 0 || decision.PolicyVersion == "" {
		t.Fatalf("unexpected allow decision: %+v, %v", decision, err)
	}
}

func TestEvaluatorDeniesDangerousInputs(t *testing.T) {
	tests := []struct {
		name   string
		change func(*remediation.PolicyInput)
		reason string
	}{
		{"production", func(in *remediation.PolicyInput) { in.Environment = "production" }, "environment_not_development"},
		{"repository", func(in *remediation.PolicyInput) { in.RepositoryAllowed = false }, "repository_not_allowed"},
		{"path", func(in *remediation.PolicyInput) { in.PathAllowed = false }, "path_not_allowed"},
		{"namespace", func(in *remediation.PolicyInput) { in.ProtectedNamespace = true }, "protected_namespace"},
		{"secret", func(in *remediation.PolicyInput) { in.SecretTarget = true }, "secret_target"},
		{"direct mutation", func(in *remediation.PolicyInput) { in.DirectMutation = true }, "direct_cluster_mutation"},
		{"delete", func(in *remediation.PolicyInput) { in.Destructive = true }, "destructive_operation"},
		{"evidence", func(in *remediation.PolicyInput) { in.EvidenceVerified = false }, "evidence_not_verified"},
		{"cause", func(in *remediation.PolicyInput) { in.RootCause = "unknown" }, "unsupported_root_cause"},
		{"operation", func(in *remediation.PolicyInput) { in.Operation = "change_image" }, "unsupported_operation"},
		{"unsafe increase", func(in *remediation.PolicyInput) { in.ResourceChangeSafe = false }, "unsafe_resource_change"},
	}
	evaluator, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := safeInput()
			test.change(&input)
			decision, err := evaluator.Evaluate(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed || !contains(decision.Reasons, test.reason) {
				t.Fatalf("unexpected denial: %+v", decision)
			}
		})
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
