CREATE TABLE remediation_proposals (
    id uuid PRIMARY KEY,
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    investigation_id uuid NOT NULL REFERENCES investigations(id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('POLICY_ALLOWED', 'POLICY_DENIED')),
    proposal jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX remediation_proposals_incident_created_idx ON remediation_proposals (incident_id, created_at DESC, id DESC);

CREATE TABLE policy_decisions (
    id uuid PRIMARY KEY,
    proposal_id uuid NOT NULL UNIQUE REFERENCES remediation_proposals(id) ON DELETE CASCADE,
    allowed boolean NOT NULL,
    policy_version text NOT NULL,
    decision jsonb NOT NULL,
    policy_input jsonb NOT NULL,
    evaluated_at timestamptz NOT NULL
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    proposal_id uuid NOT NULL REFERENCES remediation_proposals(id) ON DELETE CASCADE,
    actor text NOT NULL,
    action text NOT NULL,
    resource text NOT NULL,
    decision text NOT NULL,
    policy_version text NOT NULL,
    trace_id text,
    outcome text NOT NULL,
    event jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX audit_events_incident_created_idx ON audit_events (incident_id, created_at DESC, id DESC);
