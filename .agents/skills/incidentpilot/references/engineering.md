# Engineering standards

## Repository shape

Evolve toward this structure when implementation requires it:

```text
cmd/
  api/
  agent/
  mcp/

internal/
  agent/
  incident/
  evidence/
  mcp/
  kubernetes/
  prometheus/
  loki/
  tempo/
  argocd/
  github/
  remediation/
  policy/
  llm/
  telemetry/
  storage/

migrations/
policies/
deploy/
  helm/
  argocd/
  observability/
infrastructure/
  terraform/
    aws/
demo/
  apps/
  traffic-generator/
scenarios/
evals/
dashboards/
docs/
```

Do not create empty directories merely to satisfy this reference.

## Go practices

Follow the repository's existing conventions first.

Otherwise:
- keep interfaces near consumers when idiomatic
- pass `context.Context` through external boundaries
- set timeouts/deadlines on network/API calls
- wrap errors with useful context
- avoid global mutable state
- make dependencies explicit
- keep provider-specific code behind adapters
- prefer table-driven tests for suitable logic
- avoid premature generic abstractions

## LLM integration

Core types should represent:
- messages
- tool definitions
- tool calls
- structured response
- usage metadata if available

Adapters translate to provider APIs.

Do not let provider packages spread through:
- incident domain
- evidence domain
- MCP tool logic
- remediation policy logic

If one provider needs a workaround, isolate it in that provider.

## MCP implementation

Use the current official Go SDK.

Do not hard-code protocol-version assumptions unless the repository explicitly pins them.

For each tool:
- define a narrow schema
- validate it
- cap result size
- map backend failures to useful tool errors
- instrument execution
- test success and failure behavior

## Observability

### Traces

Create spans for:
- alert ingestion
- incident lifecycle transitions
- investigation steps
- LLM requests
- MCP requests
- downstream adapters
- policy evaluation
- remediation/PR operations

Avoid putting full prompts, secrets, large logs, or sensitive payloads into span attributes.

### Metrics

Potential metrics:

```text
incidentpilot_incidents_total
incidentpilot_investigations_total
incidentpilot_investigation_duration_seconds
incidentpilot_llm_requests_total
incidentpilot_llm_tokens_total
incidentpilot_tool_calls_total
incidentpilot_tool_errors_total
incidentpilot_policy_denials_total
incidentpilot_remediation_requests_total
incidentpilot_prs_created_total
```

Good bounded labels:
- tool
- provider
- model if model cardinality is controlled
- status
- severity

Do not label metrics with:
- incident ID
- trace ID
- pod UID
- commit SHA
- raw query
- arbitrary error messages

### Logs

Use structured logs.

Useful context:
- incident_id
- trace_id
- component
- tool
- duration
- result status

## Persistence

Use migrations.

Keep domain/storage boundaries clear.

Evidence should preserve provenance and either store or reference the raw structured result when useful.

Avoid storing secrets or unnecessary full prompt content.

## Testing layers

### Unit
- domain transitions
- evidence normalization
- hypothesis state
- budget enforcement
- provider-independent agent behavior
- policy input construction
- patch generation

### Adapter/integration
- Kubernetes fake/test cluster as appropriate
- Prometheus/Loki/Tempo API adapters
- OPA decisions
- PostgreSQL repositories
- GitHub adapter with mocks/test endpoints

### End-to-end
Use kind and deterministic fault scenarios.

### Agent evaluation
Use scenario ground truth; see `evaluation.md`.

## CI

A useful baseline:

```text
gofmt check
go vet
golangci-lint
go test
OPA/Rego tests
Helm lint/template validation
container build
```

Run targeted checks during development and the appropriate broader suite before declaring a milestone complete.

Never say tests passed if they were not run.

## Change discipline

Prefer one logical change per task/PR.

Do not:
- reformat unrelated files
- rename broad package trees unnecessarily
- introduce a framework solely because it is popular
- replace working infrastructure without a documented reason

Update docs when behavior, config, API schemas, architecture, or security assumptions change.

## Configuration

Prefer explicit configuration with safe defaults.

Separate:
- runtime config
- secrets/credentials
- provider selection
- investigation budgets
- policy/environment config

Provide example config without real credentials.

## Local first

Do not move to EKS merely to prove Kubernetes knowledge.

Local v1 should work under kind first.

Cloud deployment should exercise the same application architecture rather than introducing a separate cloud-only code path.
