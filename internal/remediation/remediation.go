package remediation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"incidentpilot/internal/agent"
	"incidentpilot/internal/evidence"
)

const (
	StatusPolicyAllowed = "POLICY_ALLOWED"
	StatusPolicyDenied  = "POLICY_DENIED"
)

var ErrInvalidProposal = errors.New("invalid remediation proposal")

var (
	dnsLabelPattern   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	pathPattern       = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,256}$`)
	operationPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	memoryPattern     = regexp.MustCompile(`^([1-9][0-9]{0,3})Mi$`)
)

type Target struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Container  string `json:"container"`
}

type Change struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Request struct {
	IncidentID      string   `json:"incident_id"`
	InvestigationID string   `json:"investigation_id"`
	Operation       string   `json:"operation"`
	Target          Target   `json:"target"`
	Change          Change   `json:"change"`
	Reason          string   `json:"reason"`
	EvidenceIDs     []string `json:"evidence_ids"`
}

type Proposal struct {
	ID              string    `json:"id"`
	IncidentID      string    `json:"incident_id"`
	InvestigationID string    `json:"investigation_id"`
	Operation       string    `json:"operation"`
	Target          Target    `json:"target"`
	Change          Change    `json:"change"`
	Reason          string    `json:"reason"`
	EvidenceIDs     []string  `json:"evidence_ids"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

type PolicyInput struct {
	Environment        string `json:"environment"`
	Repository         string `json:"repository"`
	RepositoryAllowed  bool   `json:"repository_allowed"`
	Path               string `json:"path"`
	PathAllowed        bool   `json:"path_allowed"`
	Operation          string `json:"operation"`
	Namespace          string `json:"namespace"`
	TargetKind         string `json:"target_kind"`
	TargetName         string `json:"target_name"`
	Container          string `json:"container"`
	Field              string `json:"field"`
	BeforeMiB          int    `json:"before_mib"`
	AfterMiB           int    `json:"after_mib"`
	ObservedBeforeMiB  int    `json:"observed_before_mib"`
	ProtectedNamespace bool   `json:"protected_namespace"`
	SecretTarget       bool   `json:"secret_target"`
	DirectMutation     bool   `json:"direct_mutation"`
	Destructive        bool   `json:"destructive"`
	EvidenceVerified   bool   `json:"evidence_verified"`
	RootCause          string `json:"root_cause"`
	ResourceChangeSafe bool   `json:"resource_change_safe"`
}

type PolicyDecision struct {
	ID            string    `json:"id"`
	Allowed       bool      `json:"allowed"`
	Reasons       []string  `json:"reasons"`
	PolicyVersion string    `json:"policy_version"`
	EvaluatedAt   time.Time `json:"evaluated_at"`
}

type AuditEvent struct {
	ID            string    `json:"id"`
	IncidentID    string    `json:"incident_id"`
	Actor         string    `json:"actor"`
	Action        string    `json:"action"`
	Resource      string    `json:"resource"`
	Decision      string    `json:"decision"`
	PolicyVersion string    `json:"policy_version"`
	TraceID       string    `json:"trace_id,omitempty"`
	Outcome       string    `json:"outcome"`
	CreatedAt     time.Time `json:"created_at"`
}

type Result struct {
	Proposal Proposal       `json:"proposal"`
	Decision PolicyDecision `json:"decision"`
	Audit    AuditEvent     `json:"audit"`
}

type Source interface {
	GetInvestigation(context.Context, string) (agent.Report, error)
	EvidenceForRemediation(context.Context, string, []string) ([]evidence.Record, error)
}

type Store interface {
	SaveRemediationEvaluation(context.Context, Result, PolicyInput) error
	GetRemediation(context.Context, string) (Result, error)
}

type Evaluator interface {
	Evaluate(context.Context, PolicyInput) (PolicyDecision, error)
}

type Config struct {
	Environment string
	Repository  string
	AllowedPath string
	Now         func() time.Time
}

type Service struct {
	Source    Source
	Store     Store
	Evaluator Evaluator
	Config    Config
}

func (s Service) Get(ctx context.Context, id string) (Result, error) {
	if s.Store == nil {
		return Result{}, errors.New("remediation service is not configured")
	}
	if _, err := uuid.Parse(id); err != nil {
		return Result{}, fmt.Errorf("%w: invalid remediation ID", ErrInvalidProposal)
	}
	return s.Store.GetRemediation(ctx, id)
}

func (s Service) Request(ctx context.Context, request Request) (Result, error) {
	if s.Source == nil || s.Store == nil || s.Evaluator == nil {
		return Result{}, errors.New("remediation service is not configured")
	}
	if err := validateRequest(request); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidProposal, err)
	}
	if s.Config.Environment == "" || !repositoryPattern.MatchString(s.Config.Repository) || !pathPattern.MatchString(s.Config.AllowedPath) || strings.Contains(s.Config.AllowedPath, "..") {
		return Result{}, errors.New("trusted remediation configuration is invalid")
	}
	ctx, span := otel.Tracer("incidentpilot/remediation").Start(ctx, "remediation.request")
	defer span.End()
	span.SetAttributes(attribute.String("incident.id", request.IncidentID), attribute.String("remediation.operation", request.Operation))
	report, err := s.Source.GetInvestigation(ctx, request.InvestigationID)
	if err != nil {
		return Result{}, fmt.Errorf("load investigation: %w", err)
	}
	if report.IncidentID != request.IncidentID || report.Status != agent.StatusRootCauseFound || report.RootCause == nil || report.RootCause.Cause == "" {
		return Result{}, fmt.Errorf("%w: proposal requires a verified root-cause investigation", ErrInvalidProposal)
	}
	if !sameIDs(request.EvidenceIDs, report.RootCause.EvidenceIDs) {
		return Result{}, fmt.Errorf("%w: proposal evidence must exactly match the verified root cause", ErrInvalidProposal)
	}
	records, err := s.Source.EvidenceForRemediation(ctx, request.IncidentID, request.EvidenceIDs)
	if err != nil {
		return Result{}, fmt.Errorf("verify evidence: %w", err)
	}
	if len(records) != len(request.EvidenceIDs) {
		return Result{}, fmt.Errorf("%w: proposal evidence is not persisted for the incident", ErrInvalidProposal)
	}
	observedLimit := deploymentMemoryLimit(records)
	input := derivePolicyInput(s.Config, request, report.RootCause.Cause, observedLimit)
	decision, err := s.Evaluator.Evaluate(ctx, input)
	if err != nil {
		return Result{}, err
	}
	now := time.Now().UTC()
	if s.Config.Now != nil {
		now = s.Config.Now().UTC()
	}
	decision.ID, decision.EvaluatedAt = uuid.NewString(), now
	status, outcome := StatusPolicyDenied, "denied"
	if decision.Allowed {
		status, outcome = StatusPolicyAllowed, "allowed"
	}
	proposal := Proposal{ID: uuid.NewString(), IncidentID: request.IncidentID, InvestigationID: request.InvestigationID, Operation: request.Operation, Target: request.Target, Change: request.Change, Reason: request.Reason, EvidenceIDs: append([]string(nil), request.EvidenceIDs...), Status: status, CreatedAt: now}
	traceID := ""
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		traceID = spanContext.TraceID().String()
	}
	audit := AuditEvent{ID: uuid.NewString(), IncidentID: request.IncidentID, Actor: "incidentpilot-remediation", Action: "request_remediation", Resource: request.Target.Namespace + "/" + request.Target.Kind + "/" + request.Target.Name, Decision: status, PolicyVersion: decision.PolicyVersion, TraceID: traceID, Outcome: outcome, CreatedAt: now}
	result := Result{Proposal: proposal, Decision: decision, Audit: audit}
	if err := s.Store.SaveRemediationEvaluation(ctx, result, input); err != nil {
		return Result{}, fmt.Errorf("persist remediation decision: %w", err)
	}
	requests, _ := otel.Meter("incidentpilot/remediation").Int64Counter("incidentpilot_remediation_requests_total")
	requests.Add(ctx, 1, metric.WithAttributes(attribute.String("status", outcome)))
	if !decision.Allowed {
		denials, _ := otel.Meter("incidentpilot/remediation").Int64Counter("incidentpilot_policy_denials_total")
		denials.Add(ctx, 1)
	}
	return result, nil
}

func derivePolicyInput(config Config, request Request, rootCause, observedLimit string) PolicyInput {
	before, beforeOK := memoryMiB(request.Change.Before)
	after, afterOK := memoryMiB(request.Change.After)
	observedBefore, observedOK := memoryMiB(observedLimit)
	return PolicyInput{
		Environment: config.Environment, Repository: config.Repository,
		RepositoryAllowed: request.Target.Repository == config.Repository,
		Path:              request.Target.Path, PathAllowed: request.Target.Path == config.AllowedPath,
		Operation: request.Operation, Namespace: request.Target.Namespace, TargetKind: request.Target.Kind,
		TargetName: request.Target.Name, Container: request.Target.Container, Field: request.Change.Field,
		BeforeMiB: before, AfterMiB: after, ObservedBeforeMiB: observedBefore,
		ProtectedNamespace: request.Target.Namespace != "incidentpilot-demo",
		SecretTarget:       strings.EqualFold(request.Target.Kind, "Secret") || request.Operation == "modify_secret",
		DirectMutation:     request.Operation == "direct_cluster_patch",
		Destructive:        request.Operation == "delete_resource",
		EvidenceVerified:   true, RootCause: rootCause,
		ResourceChangeSafe: beforeOK && afterOK && observedOK && before == observedBefore && after > before && after <= 512 && after <= before*4,
	}
}

func deploymentMemoryLimit(records []evidence.Record) string {
	for _, record := range records {
		if record.Tool != "kubernetes_get_deployment" || record.Source != "kubernetes/deployment" || record.ResourceRef != "incidentpilot-demo/payments-api" {
			continue
		}
		var deployment struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name      string `json:"name"`
							Resources struct {
								Limits map[string]string `json:"limits"`
							} `json:"resources"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if json.Unmarshal(record.Payload, &deployment) != nil {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "payments-api" {
				return container.Resources.Limits["memory"]
			}
		}
	}
	return ""
}

func validateRequest(request Request) error {
	if _, err := uuid.Parse(request.IncidentID); err != nil {
		return errors.New("invalid incident ID")
	}
	if _, err := uuid.Parse(request.InvestigationID); err != nil {
		return errors.New("invalid investigation ID")
	}
	if !operationPattern.MatchString(request.Operation) || !repositoryPattern.MatchString(request.Target.Repository) || !pathPattern.MatchString(request.Target.Path) || strings.Contains(request.Target.Path, "..") {
		return errors.New("invalid remediation operation or repository path")
	}
	if !dnsLabelPattern.MatchString(request.Target.Namespace) || !dnsLabelPattern.MatchString(request.Target.Name) || !dnsLabelPattern.MatchString(request.Target.Container) || (request.Target.Kind != "Deployment" && request.Target.Kind != "Secret") {
		return errors.New("invalid remediation target")
	}
	if !operationPattern.MatchString(request.Change.Field) || len(request.Change.Before) > 64 || len(request.Change.After) > 64 || !utf8.ValidString(request.Change.Before) || !utf8.ValidString(request.Change.After) {
		return errors.New("invalid remediation change")
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 512 || !utf8.ValidString(request.Reason) {
		return errors.New("remediation reason must contain 1 to 512 UTF-8 bytes")
	}
	if len(request.EvidenceIDs) < 1 || len(request.EvidenceIDs) > 8 {
		return errors.New("remediation requires 1 to 8 evidence IDs")
	}
	seen := map[string]bool{}
	for _, id := range request.EvidenceIDs {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			return errors.New("invalid or duplicate remediation evidence ID")
		}
		seen[id] = true
	}
	return nil
}

func memoryMiB(value string) (int, bool) {
	match := memoryPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, false
	}
	parsed, err := strconv.Atoi(match[1])
	return parsed, err == nil
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
