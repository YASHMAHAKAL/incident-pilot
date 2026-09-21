# Phase 5: deterministic evidence collection

The API can collect evidence for a recent incident without an LLM:

```text
incident
-> bounded incident window
-> incidentpilot-mcp read-only tools
-> normalized evidence records
-> PostgreSQL
```

Call `POST /api/v1/incidents/{id}/evidence/collect` with the existing API bearer token, then retrieve it with `GET /api/v1/incidents/{id}/evidence`. The collection response contains a collection ID, evidence records, their time window, and any tool failures. Repeating collection creates another historical collection; it never changes a Kubernetes resource.

For an allowlisted `incidentpilot-demo` incident younger than 24 hours, the collector records deployment, Service, pod, and event observations plus a bounded error-rate range, log range, and trace search. If a trace is found, it also stores that trace. When Phase 8 change sources are configured, the initial collection additionally records Argo CD application state and nearby deployment history, then reads commit metadata and a bounded diff for the newest revision in that history. Targeted downstream collections do not repeat change-source reads. Each record has a source, tool, collection time, parameters, resource reference, normalized deterministic summary, and retained structured payload. Timed evidence retains the exact window. The collector uses `started_at - 2 minutes` through resolution/current time, caps it at one hour, and rejects stale incidents rather than substituting unrelated current data.

The collector accepts results only when their MCP source matches the planned tool. It records safe partial failures instead of failing a useful collection because one telemetry backend is unavailable. Kubernetes values are already scrubbed by the MCP boundary; common credential-shaped fields and inline `token`/`password`/`authorization`/`secret` values are redacted before evidence is stored. Logs and telemetry are stored as untrusted data, never as instructions or RCA claims.

Local reproduction after creating a fresh `DemoCheckoutErrors` incident:

```sh
curl -H 'Authorization: Bearer incidentpilot-local-dev-token-v1' http://127.0.0.1:8080/api/v1/incidents
curl -X POST -H 'Authorization: Bearer incidentpilot-local-dev-token-v1' http://127.0.0.1:8080/api/v1/incidents/INCIDENT_ID/evidence/collect
curl -H 'Authorization: Bearer incidentpilot-local-dev-token-v1' http://127.0.0.1:8080/api/v1/incidents/INCIDENT_ID/evidence
```

This is evidence collection, not root-cause analysis. [Phase 6](phase-6-llm.md) adds a provider-independent LLM boundary. The bounded investigator in Phase 7 will reason over these evidence IDs but cannot treat its own text as evidence.
