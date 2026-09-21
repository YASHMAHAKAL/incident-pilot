CREATE TABLE incidents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fingerprint text NOT NULL,
    alert_name text NOT NULL,
    namespace text NOT NULL,
    service text NOT NULL,
    severity text NOT NULL,
    started_at timestamptz NOT NULL,
    detected_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    status text NOT NULL CHECK (status IN ('DETECTED', 'RESOLVED')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (fingerprint, started_at)
);
