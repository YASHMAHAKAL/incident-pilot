ALTER TABLE remediation_proposals DROP CONSTRAINT remediation_proposals_status_check;
ALTER TABLE remediation_proposals ADD CONSTRAINT remediation_proposals_status_check
    CHECK (status IN ('POLICY_ALLOWED', 'POLICY_DENIED', 'PR_CREATED', 'PR_FAILED'));

CREATE TABLE remediation_pull_requests (
    id uuid PRIMARY KEY,
    proposal_id uuid NOT NULL UNIQUE REFERENCES remediation_proposals(id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('PR_CREATED', 'PR_FAILED')),
    repository text NOT NULL,
    base_branch text NOT NULL,
    head_branch text NOT NULL,
    pull_request jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
