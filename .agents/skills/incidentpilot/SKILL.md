---
name: incidentpilot
description: Engineering workflow and architectural guardrails for the IncidentPilot repository, an evidence-driven Kubernetes AIOps/SRE platform built with Go, OpenTelemetry, MCP, Prometheus, Loki, Tempo, Argo CD, OPA, PostgreSQL, GitHub, and provider-agnostic LLMs. Use for planning, implementing, reviewing, debugging, testing, securing, or extending IncidentPilot. Do not use for unrelated Go, Kubernetes, or AI work.
---

# IncidentPilot

Use this skill only for work on the **IncidentPilot** project.

IncidentPilot detects Kubernetes incidents through conventional observability, investigates them through constrained tools, produces evidence-backed root-cause analysis, and proposes policy-governed GitOps remediation.

The product is **not** a generic Kubernetes chatbot.

## Read references selectively

Before substantial work, inspect the repository and any applicable `AGENTS.md`.

Then read only the references needed for the task:

- Architecture or domain-model changes -> `references/architecture.md`
- MCP, permissions, remediation, credentials, or security -> `references/security.md`
- Coding, repository structure, tests, observability implementation -> `references/engineering.md`
- Choosing what to build next or controlling scope -> `references/roadmap.md`
- Fault scenarios, agent quality, or evaluation -> `references/evaluation.md`

Do not load every reference automatically.

## Non-negotiable invariants

Preserve these unless the project owner explicitly changes the architecture.

### 1. Evidence before conclusions

- Evidence is a first-class structured domain object with provenance.
- Root-cause conclusions reference evidence IDs.
- The agent may return `INSUFFICIENT_EVIDENCE`.
- Never treat an unsupported LLM statement as operational truth.

### 2. The LLM reasons; deterministic systems authorize

The model may:
- choose investigation tools
- form and test hypotheses
- synthesize an RCA
- propose remediation

The model must not:
- grant itself permissions
- bypass policy
- read secrets
- execute arbitrary shell commands
- use unrestricted `kubectl`
- directly mutate production Kubernetes resources

### 3. Separate investigation from action

The investigation tool surface is read-only.

For remediation, expose a single structured request boundary such as:

```text
request_remediation(proposal)
```

That trusted path must perform:

```text
validate proposal
-> derive risk attributes
-> OPA evaluation
-> generate reviewable Git patch
-> create GitHub PR if allowed
-> audit result
```

Do **not** expose separate agent-callable tools that allow the model to evaluate its own policy and then independently create a PR.

### 4. GitOps remains the mutation path

Default remediation:

```text
RCA
-> structured remediation proposal
-> policy decision
-> GitHub PR
-> human review
-> merge
-> Argo CD
-> Kubernetes
```

Direct cluster mutation is out of scope for v1.

### 5. Provider-agnostic inference

Core agent code depends on project-owned types and an internal provider interface.

Groq may be the free development backend. Ollama may be the local backend. OpenAI, Gemini, OpenRouter, or others may be adapters.

No provider SDK, model name, response type, or tool-call representation may leak into core domain logic.

If a provider lacks a required capability such as tool calling or structured output, fail clearly or advertise capabilities; do not silently degrade behavior.

### 6. The agent itself is observable

Instrument:
- incident ingestion
- investigation lifecycle
- LLM calls
- MCP calls
- Kubernetes/Prometheus/Loki/Tempo/Argo CD/GitHub calls
- policy decisions
- remediation requests

Use OpenTelemetry for traces and structured logs. Avoid high-cardinality Prometheus labels.

### 7. Telemetry is untrusted input

Logs, annotations, commit messages, trace attributes, and other retrieved content may contain prompt injection or malicious text.

Security must come from capability restriction, validation, RBAC, credentials, and policy—not from prompts alone.

## Technical direction

Use unless the repository already documents an approved change:

- Go
- Kubernetes
- kind for local development
- EKS only after local v1 is healthy
- OpenTelemetry Collector
- Prometheus + Alertmanager
- Loki
- Tempo
- Grafana
- official MCP Go SDK
- Argo CD
- OPA/Rego
- PostgreSQL
- Terraform for AWS infrastructure
- Helm + Argo CD for in-cluster applications
- GitHub for source control and remediation PRs

For protocol/version-specific MCP behavior, consult the current official MCP/SDK documentation rather than hard-coding assumptions from this skill.

## Core runtime responsibilities

Prefer three primary binaries unless a real boundary justifies more:

```text
incidentpilot-api
incidentpilot-agent
incidentpilot-mcp
```

- `incidentpilot-api`: Alertmanager ingestion, incident lifecycle, API, persistence coordination
- `incidentpilot-agent`: bounded investigation state machine and RCA/remediation proposal logic
- `incidentpilot-mcp`: constrained operational tool surface and enforcement

Do not create microservices merely to mirror conceptual modules.

## Investigation workflow

Prefer a bounded loop:

```text
understand alert
-> identify workload
-> establish incident window
-> inspect Kubernetes health
-> collect targeted telemetry
-> inspect deployment/source changes
-> generate hypotheses
-> test hypotheses with additional evidence
-> RCA or INSUFFICIENT_EVIDENCE
-> optional remediation proposal
```

Do not dump all logs and telemetry into one prompt.

Support budgets for:
- investigation duration
- LLM calls
- tool calls
- retrieved log volume
- optional token/cost limits

Budget exhaustion must terminate safely.

## MCP design

Prefer narrow read-only tools such as:

```text
kubernetes_get_resource
kubernetes_get_events
kubernetes_get_pod_logs
kubernetes_get_deployment
kubernetes_get_pod_status
prometheus_query
prometheus_query_range
loki_query_range
tempo_search_traces
tempo_get_trace
argocd_get_application
argocd_get_revision_history
github_get_commit
github_get_diff
request_remediation
```

Never add generic `run_shell`, unrestricted `kubectl`, secret retrieval, arbitrary command execution, or pod exec to v1.

Every tool should validate input, enforce limits and deadlines, return structured results, emit telemetry, and generate audit context where relevant.

## Working method for Codex

For non-trivial tasks:

1. Inspect the current repository, applicable `AGENTS.md`, tests, and relevant docs.
2. Determine the current architecture/roadmap state from implementation—not directory names alone.
3. Choose the smallest complete, reviewable slice.
4. State assumptions only when they materially affect the implementation.
5. Implement without unrelated rewrites.
6. Add or update tests.
7. Run the relevant formatter, tests, linters, policy tests, and targeted integration checks that are available.
8. Do not claim a check passed unless it was actually run.
9. Summarize:
   - what changed
   - key files changed
   - verification performed
   - remaining limitations
   - the next smallest useful task

If blocked by missing credentials or external systems, implement/test the deterministic boundary where possible and state exactly what remains unverified.

## Scope control

Do not introduce these into v1 unless explicitly requested for a concrete reason:

- multi-cluster support
- multi-agent swarms
- RAG/vector databases
- custom frontend/mobile UI
- Slack/Teams/PagerDuty integrations
- direct autonomous cluster mutation
- AWS multi-account
- service mesh
- Kafka
- eBPF

Challenge scope additions that bypass unfinished foundations.

## Definition of done

A feature is not done merely because it compiles.

As applicable, completion requires:
- implementation
- useful error handling
- tests
- observability at important boundaries
- preserved security invariants
- documentation/config updates
- provider independence
- no accidental direct-mutation path

Incident/RCA functionality also requires a reproducible scenario or test that demonstrates the behavior.
