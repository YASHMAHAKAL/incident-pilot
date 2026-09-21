# Phase 7: bounded investigator

`incidentpilot-agent` is a one-shot command. Given an existing incident UUID, it collects current incident evidence through the read-only MCP boundary, asks the configured LLM whether another demo workload needs evidence, and permits at most two targeted collections. It then requests structured hypotheses and an evidence-linked suspected cause. The report is persisted in PostgreSQL and printed as JSON. `-report-id` retrieves a saved report without an LLM call.

Budgets are fixed at two minutes, at most four physical LLM attempts (normally up to two planning calls plus one final analysis, with one optional 429 retry), two targeted collections, and bounded evidence plans. A normal workload produces eight or nine observations depending on trace availability; orders-api may add its single allowlisted ConfigMap field and one 20-line pod-log tail when a collected pod is already in `CrashLoopBackOff`. Initial collection may add up to four change-intelligence observations. The LLM sees brief evidence cards with projected diagnostic fields; arbitrary Loki payloads and arbitrary trace attributes are not copied into its prompt. Trace cards contain at most 24 spans and only service, span name/kind, duration, HTTP status, and span status. The bounded CrashLoopBackOff pod-log tail is included as untrusted diagnostic data. The only model-requestable operation is `collect_workload_evidence` for an unvisited downstream demo workload, with `error_rate`, `heap_bytes`, or `success_latency_avg`; trusted code validates the target and persists evidence with provenance before using it. Change-source selection and crash-pod log selection are deterministic, not model-callable. There is no shell, secret, mutation, or remediation operation.

The advertised tool schema contains only unvisited downstream workloads. If the model does not request a target while the initial symptom remains unexplained, trusted code collects the next downstream workload in the fixed demo graph. A repeated workload request also falls back to the next unvisited workload without duplicate collection. If collected Kubernetes facts already satisfy a deterministic verifier, the agent skips further planning and proceeds to final analysis. Invalid or unauthorized targets still terminate the run with `INSUFFICIENT_EVIDENCE`.

Phase 7 established the OOM verifier. Phase 9 adds verifiers for bad images, invalid order configuration, broken Service selectors, CPU/latency regression, and trace-visible payment failure. In every case the conclusion is constructed from corroborating records, not copied from model text. Other outcomes, missing citations, tool errors, and budget exhaustion produce `INSUFFICIENT_EVIDENCE`; model hypotheses remain explicitly labeled hypotheses. See [Phase 9](phase-9-rca.md) for the expanded rules.

If a provider call fails, the persisted `reason` includes a bounded category such as `provider_http_400`, `transport`, `timeout`, or `invalid_response`. Provider response bodies, raw error text, prompts, and credentials are not stored in the report. An allowlisted Groq 400 subtype may be included, such as `tool_generation_failed`; arbitrary provider text is never saved. One HTTP 429 retry is permitted only when `Retry-After` is positive, at most 20 seconds, and fits the investigation deadline and call budget. HTTP 400 is never retried automatically. Reports count physical calls, retries, and successful-call token usage.

Planning requests allow 1024 completion tokens and final analysis allows 1536. Groq GPT-OSS defaults to low reasoning effort; set `INCIDENTPILOT_GROQ_REASONING_EFFORT` to `medium` or `high` only after measuring output quality and token usage. Final analysis uses a schema-constrained response where the configured provider supports it. Schema validity does not bypass the evidence verifier.

Invalid provider output is classified further using fixed internal validation-rule codes, for example `invalid_response_completion_truncated` or `invalid_response_invalid_tool_arguments`. These codes do not include model-generated values or raw responses.

When the model's final fields do not satisfy a deterministic verifier, `reason` includes a bounded verification category (`unsupported_component`, `unsupported_cause`, `missing_cited_oom_or_limit`, `missing_cited_image_pull_evidence`, `missing_cited_invalid_config_evidence`, `missing_cited_service_routing_evidence`, `missing_cited_latency_trace_evidence`, or `missing_cited_payment_trace_evidence`). A supported hypothesis alone is not a verified root cause; the final response must name a supported cause and cite every required record.

## Local run

Run against the dedicated kind cluster with fresh incident data. In separate terminals, forward PostgreSQL and MCP:

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/postgres 15432:5432
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/incidentpilot-mcp 18081:8081
```

Copy `.env.example` to `.env` if it is not already present, then put your Groq key in `INCIDENTPILOT_LLM_API_KEY`. The local `.env` file is Git-ignored. Load it into the current shell before starting the agent:

```sh
set -a
source .env
set +a
```

The included local configuration uses Groq. For Ollama, set `INCIDENTPILOT_LLM_PROVIDER=ollama`, supply a locally available tool-capable model, and clear `INCIDENTPILOT_LLM_API_KEY`:

```sh
# Edit .env, then reload it.
set -a
source .env
set +a
make build
./bin/incidentpilot-agent -incident-id INCIDENT_UUID
./bin/incidentpilot-agent -report-id REPORT_UUID
```

To exercise the OOM path, inject and check the `payments-api` OOM fixture, wait for a fresh `DemoCheckoutErrors` frontend incident, then run the agent with that incident UUID. The bad-image path has its own `DemoImagePullBackOff` orders-api alert; see [its scenario instructions](../scenarios/bad-image/README.md). Always reset either fixture afterward. The system does not automatically launch an investigation on every webhook and does not deploy a continuously running agent in kind. No live provider call is part of `make check`; adapter and investigator tests use fixtures.

## Manual OOM evaluation (2026-09-21)

Three paced runs against fresh kind OOM incidents using Groq `openai/gpt-oss-20b` and the default low reasoning effort each returned `ROOT_CAUSE_FOUND`, citing the payments-api `OOMKilled` pod and 48Mi Deployment records. The persisted investigation IDs were `bf157bc4-b1b0-4ba8-bfab-c10d9aaa75ac`, `fa874cab-0604-41c2-81bd-335f233a86d4`, and `351569fa-b89b-4346-9f45-79e2454a5f5e`. Each run used three physical LLM calls, zero retries, and 24 MCP observations. This is a 3/3 OOM smoke result, not a reliability guarantee or an evaluation of the other five scenarios. The fixture was reset after each run.
