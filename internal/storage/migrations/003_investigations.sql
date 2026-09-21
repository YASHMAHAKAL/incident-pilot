CREATE TABLE investigations (
    id uuid PRIMARY KEY,
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('ROOT_CAUSE_FOUND', 'INSUFFICIENT_EVIDENCE')),
    started_at timestamptz NOT NULL,
    completed_at timestamptz NOT NULL,
    report jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX investigations_incident_created_idx ON investigations (incident_id, created_at DESC, id DESC);
