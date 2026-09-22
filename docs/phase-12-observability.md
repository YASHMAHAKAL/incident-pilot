# Phase 12: observe the observer

IncidentPilot emits OpenTelemetry traces and metrics for its own control plane. The API, one-shot agent, MCP server, backend HTTP clients, LLM boundary, policy evaluator, remediation service, PostgreSQL stores, and GitHub adapter all participate in the telemetry path. Prompts, credentials, evidence payloads, raw queries, and provider response bodies are never span attributes or metric labels.

## Asynchronous trace continuity

Alert ingestion captures the active W3C `traceparent` and `tracestate` after authentication and validation. PostgreSQL stores that propagation state with the incident. When the one-shot agent later loads the incident, it restores the remote context before starting `incident.investigate`. This keeps the alert and delayed investigation under one trace ID without requiring an in-memory process or message broker. The public incident representation exposes only the trace ID; parent span IDs and trace state remain internal.

HTTP client and server instrumentation propagates context from the agent through MCP and from MCP into Kubernetes, Prometheus, Loki, Tempo, Argo CD, GitHub, and the remediation API. The MCP HTTP server now extracts W3C headers before creating tool spans. Investigation reports persist their trace ID so operators can open the exact trace in Tempo.

## Metrics

The local Prometheus endpoint receives bounded-label control-plane metrics, including:

- `incidentpilot_alert_signals_total`
- `incidentpilot_investigations_total{status}`
- `incidentpilot_investigation_duration_seconds{status}`
- `incidentpilot_llm_requests_total{provider,model}`
- `incidentpilot_llm_request_duration_seconds{provider,model,status}`
- `incidentpilot_llm_tokens_total{provider,model,direction}`
- `incidentpilot_tool_calls_total{tool,status}`
- `incidentpilot_tool_errors_total{tool}`
- `incidentpilot_policy_denials_total`
- `incidentpilot_remediation_requests_total{status}`
- `incidentpilot_prs_created_total`

Incident IDs, trace IDs, pod names, commit SHAs, queries, and arbitrary errors are deliberately excluded from labels.

## Local inspection

Open Grafana as described in the README and select the provisioned **IncidentPilot control plane** dashboard. In Explore, select Tempo and search for the trace ID returned on the incident or investigation report. A normal investigation contains `incident.ingest`, `incident.investigate`, `llm.chat`, MCP HTTP/tool spans, evidence persistence, and backend HTTP spans. A remediation request additionally contains `remediation.request`, `policy.evaluate`, and `github.create_pr` when GitHub execution is enabled.

The agent remains an explicit one-shot command in local v1; Phase 12 correlates that delayed work but does not claim automatic alert-to-agent scheduling.
