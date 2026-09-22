ALTER TABLE incidents ADD COLUMN trace_id text;
ALTER TABLE incidents ADD COLUMN traceparent text;
ALTER TABLE incidents ADD COLUMN tracestate text;

ALTER TABLE incidents ADD CONSTRAINT incidents_trace_id_format
    CHECK (trace_id IS NULL OR trace_id ~ '^[0-9a-f]{32}$');
ALTER TABLE incidents ADD CONSTRAINT incidents_traceparent_size
    CHECK (traceparent IS NULL OR length(traceparent) <= 128);
ALTER TABLE incidents ADD CONSTRAINT incidents_tracestate_size
    CHECK (tracestate IS NULL OR length(tracestate) <= 512);
