# IncidentPilot architecture

## Product thesis

IncidentPilot is an evidence-driven, policy-governed AIOps/SRE platform for Kubernetes.

Conventional observability detects incidents. An AI investigator uses constrained operational tools to gather evidence, correlate telemetry with deployment and source-control changes, test hypotheses, and produce an evidence-backed root cause. Remediation is proposed by the agent but authorized and executed through deterministic policy/GitOps machinery.

The differentiators are:

1. evidence-first RCA
2. constrained operational capabilities
3. policy-governed GitOps remediation
4. full observability of the agent itself
5. provider-agnostic inference
6. reproducible evaluation

## Logical architecture

```text
Instrumented workloads
        |
        | OTLP / metrics
        v
OpenTelemetry Collector
   |        |        |
Prometheus  Loki    Tempo
   |
Alertmanager
   |
   v
incidentpilot-api
   |
   v
incidentpilot-agent
   |
   | MCP
   v
incidentpilot-mcp
   |
   +--> Kubernetes read APIs
   +--> Prometheus
   +--> Loki
   +--> Tempo
   +--> Argo CD
   +--> GitHub read APIs
   |
   +--> request_remediation(proposal)
             |
             v
       trusted remediation pipeline
             |
       validate / derive risk
             |
             v
            OPA
             |
         allow / deny
             |
             v
        Git patch + PR
             |
             v
           GitHub
             |
             v
           Argo CD
             |
             v
         Kubernetes
```

## Trust boundaries

### LLM / investigator

The model is trusted for reasoning quality only.

It can select allowed tools, form hypotheses, and propose remediation. It cannot grant permission or directly control privileged infrastructure.

### MCP server

The MCP server is a capability boundary. It exposes intentionally narrow operations, validates inputs, enforces limits, and provides structured results.

Read tools are separated from the remediation request path.

### Evidence store

Evidence is independently collected and stored/referenced with provenance. Conclusions point to evidence IDs.

### Remediation pipeline

The remediation pipeline is trusted application code, not model logic.

It:
1. validates a structured proposal
2. derives security/risk properties deterministically where possible
3. evaluates OPA
4. creates a narrow Git patch
5. creates a GitHub PR only if allowed
6. emits an audit record

The model must not choose its effective authorization level.

### GitOps

Git is the source of truth for v1 mutations. Argo CD applies merged state to Kubernetes.

## Primary binaries

### incidentpilot-api

Responsibilities:
- receive Alertmanager webhooks
- normalize/deduplicate incoming incident signals
- create/update incident records
- expose incident/report APIs
- coordinate persistence
- initiate investigations

### incidentpilot-agent

Responsibilities:
- bounded investigation lifecycle
- planning and hypothesis generation
- choosing read-only tools
- evaluating returned evidence
- determining RCA vs insufficient evidence
- creating a structured remediation proposal

### incidentpilot-mcp

Responsibilities:
- expose operational tools
- validate inputs
- enforce resource/time/result limits
- perform read-only source queries
- route remediation requests into the trusted remediation pipeline
- produce telemetry/audit context

Keep conceptual packages inside these binaries until a separate deployment boundary is justified.

## Domain model

### Incident

At minimum:

```text
ID
affected service/workload/namespace
alert name
severity
started_at
detected_at
status
evidence references
hypotheses
root cause
remediation reference
trace ID
```

Suggested lifecycle:

```text
DETECTED
-> INVESTIGATING
-> ROOT_CAUSE_FOUND
-> REMEDIATION_PROPOSED
-> POLICY_EVALUATED
-> PR_CREATED
-> RESOLVED
```

Additional states should support:
- `INSUFFICIENT_EVIDENCE`
- investigation failure
- cancelled/expired investigations where needed

### Evidence

Evidence should contain enough provenance to be inspected independently.

Useful fields:

```text
ID
incident ID
source type
collected_at
start/end time window
query or lookup parameters
normalized summary
resource reference
trace ID if relevant
raw-result reference or durable payload reference
```

Sources include:
- Kubernetes event/object
- application log
- Prometheus result
- trace
- Argo CD revision/deployment history
- Git commit/diff

Do not store only model-authored summaries when the underlying structured result can be retained or referenced.

### Hypothesis

Track at least:
- statement
- status: proposed / supported / rejected / unresolved
- evidence IDs supporting/rejecting it
- optional confidence as model output, never as authorization input

### Root cause

Must include:
- concise conclusion
- affected component
- evidence IDs
- unresolved uncertainty where applicable

### Remediation proposal

Represent structurally:

```text
incident ID
target repository/path/resource
operation
before/after values where applicable
reason
supporting evidence IDs
environment
resource category
requested effect
```

Risk-relevant attributes used by policy should be derived/validated by trusted code, not accepted blindly from the model.

## LLM provider boundary

Core packages use project-owned types.

Conceptually:

```go
type LLMProvider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
```

The exact interface may evolve, but provider-specific types remain in adapters.

Adapters may include:
- Groq
- Ollama
- OpenAI
- Gemini
- OpenRouter

Consider an explicit capabilities model if different providers vary on:
- tool calling
- structured output
- streaming
- usage/token reporting

Configuration selects the provider.

## Persistence

Use PostgreSQL initially.

Likely tables/domains:
- incidents
- evidence
- hypotheses
- investigations
- tool executions
- root causes
- remediation proposals
- policy decisions
- audit events

A vector database is not required for v1.

If incident-memory retrieval is added later, prefer evaluating PostgreSQL/pgvector before adding a separate vector service.

## Observability architecture

Instrument both demo workloads and IncidentPilot.

One investigation should ideally produce a trace shaped like:

```text
incident.receive
-> incident.investigate
   -> llm.request
   -> mcp.call
      -> prometheus.query_range
   -> llm.request
   -> mcp.call
      -> kubernetes.events
   -> mcp.call
      -> github.diff
   -> llm.request
   -> remediation.request
      -> policy.evaluate
      -> github.create_pr
```

Use trace/log correlation. Keep high-cardinality IDs out of metric labels.

## Deployment model

Development:
- Docker
- kind
- Helm
- Argo CD
- local observability stack

Cloud only after local v1:
- Terraform
- AWS VPC/networking
- EKS
- managed nodes
- IAM
- Argo CD bootstrap

Terraform manages cloud infrastructure.
Argo CD manages in-cluster application state.
