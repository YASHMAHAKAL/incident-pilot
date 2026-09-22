package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"

	"incidentpilot/internal/evidence"
	"incidentpilot/internal/incident"
	"incidentpilot/internal/remediation"
)

func (store *Postgres) EvidenceForRemediation(ctx context.Context, incidentID string, evidenceIDs []string) ([]evidence.Record, error) {
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 8 {
		return nil, nil
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text,collection_id::text,incident_id::text,tool,source,collected_at,window_start,window_end,parameters,summary,resource_ref,COALESCE(trace_id,''),payload FROM evidence WHERE incident_id=$1::uuid AND id::text=ANY($2::text[])`, incidentID, evidenceIDs)
	if err != nil {
		return nil, fmt.Errorf("verify remediation evidence: %w", err)
	}
	defer rows.Close()
	records := make([]evidence.Record, 0, len(evidenceIDs))
	for rows.Next() {
		record, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate remediation evidence: %w", err)
	}
	return records, nil
}

func (store *Postgres) SaveRemediationEvaluation(ctx context.Context, result remediation.Result, input remediation.PolicyInput) error {
	proposalJSON, err := json.Marshal(result.Proposal)
	if err != nil {
		return errors.New("encode remediation proposal")
	}
	decisionJSON, err := json.Marshal(result.Decision)
	if err != nil {
		return errors.New("encode policy decision")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return errors.New("encode policy input")
	}
	auditJSON, err := json.Marshal(result.Audit)
	if err != nil {
		return errors.New("encode audit event")
	}
	var pullRequestJSON []byte
	if result.PullRequest != nil {
		pullRequestJSON, err = json.Marshal(result.PullRequest)
		if err != nil {
			return errors.New("encode remediation pull request")
		}
	}
	for _, payload := range [][]byte{proposalJSON, decisionJSON, inputJSON, auditJSON, pullRequestJSON} {
		if len(payload) > 64<<10 {
			return errors.New("remediation record too large")
		}
	}
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "remediation.save")
	defer span.End()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin remediation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO remediation_proposals (id,incident_id,investigation_id,status,proposal,created_at) VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5::jsonb,$6)`, result.Proposal.ID, result.Proposal.IncidentID, result.Proposal.InvestigationID, result.Proposal.Status, proposalJSON, result.Proposal.CreatedAt); err != nil {
		return fmt.Errorf("insert remediation proposal: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO policy_decisions (id,proposal_id,allowed,policy_version,decision,policy_input,evaluated_at) VALUES ($1::uuid,$2::uuid,$3,$4,$5::jsonb,$6::jsonb,$7)`, result.Decision.ID, result.Proposal.ID, result.Decision.Allowed, result.Decision.PolicyVersion, decisionJSON, inputJSON, result.Decision.EvaluatedAt); err != nil {
		return fmt.Errorf("insert policy decision: %w", err)
	}
	if result.PullRequest != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO remediation_pull_requests (id,proposal_id,status,repository,base_branch,head_branch,pull_request,created_at) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7::jsonb,$8)`, result.PullRequest.ID, result.Proposal.ID, result.PullRequest.Status, result.PullRequest.Repository, result.PullRequest.BaseBranch, result.PullRequest.HeadBranch, pullRequestJSON, result.PullRequest.CreatedAt); err != nil {
			return fmt.Errorf("insert remediation pull request: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (id,incident_id,proposal_id,actor,action,resource,decision,policy_version,trace_id,outcome,event,created_at) VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11::jsonb,$12)`, result.Audit.ID, result.Audit.IncidentID, result.Proposal.ID, result.Audit.Actor, result.Audit.Action, result.Audit.Resource, result.Audit.Decision, result.Audit.PolicyVersion, result.Audit.TraceID, result.Audit.Outcome, auditJSON, result.Audit.CreatedAt); err != nil {
		return fmt.Errorf("insert remediation audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remediation transaction: %w", err)
	}
	return nil
}

func (store *Postgres) GetRemediation(ctx context.Context, id string) (remediation.Result, error) {
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "remediation.get")
	defer span.End()
	var proposalJSON, decisionJSON, auditJSON []byte
	var pullRequestJSON []byte
	err := store.pool.QueryRow(ctx, `SELECT p.proposal,d.decision,COALESCE(r.pull_request,'null'::jsonb),a.event FROM remediation_proposals p JOIN policy_decisions d ON d.proposal_id=p.id LEFT JOIN remediation_pull_requests r ON r.proposal_id=p.id JOIN audit_events a ON a.proposal_id=p.id WHERE p.id=$1::uuid`, id).Scan(&proposalJSON, &decisionJSON, &pullRequestJSON, &auditJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return remediation.Result{}, incident.ErrNotFound
	}
	if err != nil {
		return remediation.Result{}, fmt.Errorf("get remediation: %w", err)
	}
	var result remediation.Result
	if json.Unmarshal(proposalJSON, &result.Proposal) != nil || json.Unmarshal(decisionJSON, &result.Decision) != nil || json.Unmarshal(pullRequestJSON, &result.PullRequest) != nil || json.Unmarshal(auditJSON, &result.Audit) != nil {
		return remediation.Result{}, errors.New("decode remediation result")
	}
	return result, nil
}
