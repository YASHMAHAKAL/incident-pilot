package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/open-policy-agent/opa/v1/rego"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"incidentpilot/internal/remediation"
	"incidentpilot/policies"
)

type Evaluator struct {
	query rego.PreparedEvalQuery
}

func New(ctx context.Context) (*Evaluator, error) {
	query, err := rego.New(
		rego.Query("data.incidentpilot.remediation.decision"),
		rego.Module("remediation.rego", policies.Remediation),
		rego.StrictBuiltinErrors(true),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile remediation policy: %w", err)
	}
	return &Evaluator{query: query}, nil
}

func (e *Evaluator) Evaluate(ctx context.Context, input remediation.PolicyInput) (remediation.PolicyDecision, error) {
	if e == nil {
		return remediation.PolicyDecision{}, errors.New("policy evaluator is not configured")
	}
	ctx, span := otel.Tracer("incidentpilot/policy").Start(ctx, "policy.evaluate")
	defer span.End()
	results, err := e.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return remediation.PolicyDecision{}, fmt.Errorf("evaluate remediation policy: %w", err)
	}
	if len(results) != 1 || len(results[0].Expressions) != 1 {
		return remediation.PolicyDecision{}, errors.New("remediation policy returned no single decision")
	}
	value, ok := results[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return remediation.PolicyDecision{}, errors.New("remediation policy returned an invalid decision")
	}
	allowed, ok := value["allow"].(bool)
	if !ok {
		return remediation.PolicyDecision{}, errors.New("remediation policy omitted allow")
	}
	reasonValues, ok := value["reasons"].([]any)
	if !ok {
		return remediation.PolicyDecision{}, errors.New("remediation policy omitted reasons")
	}
	reasons := make([]string, 0, len(reasonValues))
	for _, value := range reasonValues {
		reason, ok := value.(string)
		if !ok || reason == "" || len(reason) > 128 {
			return remediation.PolicyDecision{}, errors.New("remediation policy returned an invalid reason")
		}
		reasons = append(reasons, reason)
	}
	if allowed && len(reasons) != 0 || !allowed && len(reasons) == 0 {
		return remediation.PolicyDecision{}, errors.New("remediation policy returned an inconsistent decision")
	}
	span.SetAttributes(attribute.Bool("policy.allowed", allowed), attribute.String("policy.version", policies.RemediationVersion))
	return remediation.PolicyDecision{Allowed: allowed, Reasons: reasons, PolicyVersion: policies.RemediationVersion}, nil
}
