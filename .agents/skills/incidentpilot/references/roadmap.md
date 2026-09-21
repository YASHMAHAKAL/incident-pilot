# IncidentPilot roadmap

Use this to decide what to build next.

Do not infer completion from directories alone. Inspect working code, tests, manifests, and observable behavior.

## Phase 0 — Repository foundation

Build:
- Go module
- baseline `cmd/` / `internal/` structure as needed
- config approach
- Makefile/task runner
- lint/test baseline
- container build

Exit:
- repeatable local developer workflow

## Phase 1 — Observable demo workload

Build a small service graph, for example:

```text
frontend -> orders-api -> payments-api -> postgres
```

Add:
- traffic generator
- OpenTelemetry instrumentation
- OTel Collector
- Prometheus
- Loki
- Tempo
- Grafana

Exit:
- application metrics, logs, and distributed traces are visible locally

## Phase 2 — Reproducible failures

Required v1 scenarios:
1. memory-limit regression -> OOMKilled
2. bad image -> ImagePullBackOff
3. invalid ConfigMap value -> application failure
4. broken Service selector -> traffic failure
5. CPU/latency regression
6. downstream latency/failure visible through traces

Each needs apply/inject, reset, and documented ground truth.

Exit:
- failures are deterministic enough for evaluation

## Phase 3 — Incident ingestion

Add:
- Prometheus alert rules
- Alertmanager
- webhook ingestion
- incident domain
- PostgreSQL persistence
- incident API

Exit:
- injecting a known failure automatically creates an incident

## Phase 4 — MCP investigation layer

Implement read-only tools for:
- Kubernetes
- Prometheus
- Loki
- Tempo

Exit:
- deterministic evidence can be gathered through typed tools without shell access

## Phase 5 — Evidence engine

Add:
- evidence domain
- provenance
- persistence
- incident time-window handling
- normalized summaries

Exit:
- evidence collection works without an LLM

## Phase 6 — LLM provider abstraction

Add:
- project-owned request/response/tool-call types
- provider interface
- Groq adapter
- Ollama adapter

Optional later:
- OpenAI
- Gemini
- OpenRouter

Exit:
- provider can be changed by config without changing agent domain logic

## Phase 7 — Investigator

Add bounded:
- planning
- observation
- hypothesis creation
- targeted evidence requests
- hypothesis verification/rejection
- RCA or insufficient-evidence outcome

Exit:
- agent correctly handles the OOM scenario from collected evidence

## Phase 8 — Change intelligence

Add:
- Argo CD application/revision history
- GitHub commit/diff reads

Exit:
- incident timing can be correlated with deployment and source changes

## Phase 9 — Evidence-backed RCA

Require:
- evidence IDs in final conclusions
- uncertainty handling
- no unsupported final RCA

Exit:
- an incident report is auditable back to collected evidence

## Phase 10 — Policy-governed remediation

Add:
- structured remediation proposal
- trusted validation/risk derivation
- OPA/Rego
- policy audit record
- single remediation request boundary

Exit:
- safe PR-type actions can be allowed while dangerous actions are denied

## Phase 11 — GitHub remediation PR

Add:
- constrained patch generation
- branch/commit
- PR creation
- PR body with incident and evidence references

Exit:
- the OOM scenario can produce a reviewable policy-approved fix PR

## Phase 12 — Observe the observer

Instrument IncidentPilot itself end to end.

Exit:
- Tempo can show one investigation across alert -> agent -> LLM -> MCP -> backend -> policy -> GitHub

## Phase 13 — Evaluation

Add:
- machine-readable scenario ground truth
- automated scenario runner
- prompt-injection/safety tests
- result reporting

Exit:
- `make eval` or equivalent produces reproducible results

## Phase 14 — AWS

Only after local v1 is healthy.

Terraform:
- VPC
- public/private subnets
- routing/NAT/IGW as required
- EKS
- managed nodes
- IAM
- security groups

Bootstrap Argo CD; let GitOps manage in-cluster applications.

Exit:
- production-like deployment with the same application architecture

## Post-v1 backlog

Only after core quality is strong:
- incident memory / pgvector
- extra providers
- notifications/chat integrations
- multi-cluster
- more complex remediation approval workflows
- cloud-resource evidence sources
- cost/security signal correlation

Do not add these simply to increase the technology count.
