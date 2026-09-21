CREATE TABLE evidence (
    id uuid PRIMARY KEY,
    collection_id uuid NOT NULL,
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    tool text NOT NULL,
    source text NOT NULL,
    collected_at timestamptz NOT NULL,
    window_start timestamptz,
    window_end timestamptz,
    parameters jsonb NOT NULL,
    summary text NOT NULL,
    resource_ref text NOT NULL,
    trace_id text,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT evidence_window_pair CHECK ((window_start IS NULL) = (window_end IS NULL)),
    CONSTRAINT evidence_window_order CHECK (window_start IS NULL OR window_start < window_end),
    UNIQUE (collection_id, tool)
);
CREATE INDEX evidence_incident_collected_idx ON evidence (incident_id, collected_at DESC, id DESC);
