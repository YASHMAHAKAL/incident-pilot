# IncidentPilot

IncidentPilot is an evidence-driven Kubernetes incident investigation platform.
It turns conventional alerts into bounded, evidence-backed root-cause analysis
(RCA), then sends only policy-approved remediation proposals through a GitOps
pull-request workflow.

It is deliberately **not** an unrestricted Kubernetes chatbot: the LLM can
reason about collected evidence, but it cannot execute shell commands, read
Secrets, run `kubectl`, change a cluster, bypass OPA, or create a PR directly.

## Why this project exists

Kubernetes alerts often identify a symptom rather than the cause. An increase
in checkout failures, for example, could originate in the frontend, a failing
downstream service, a bad image, a Service selector, or an under-sized memory
limit. IncidentPilot provides a constrained path from alert to reviewable
action:

1. Alertmanager sends a conventional alert to the API.
2. A one-shot investigator gathers small, scoped observations through MCP.
3. Deterministic verification accepts an RCA only when its evidence matches a
   supported cause; otherwise it records `INSUFFICIENT_EVIDENCE`.
4. A structured remediation proposal is validated by trusted code and OPA.
5. An allowed change can become one narrow GitHub pull request for human review.

Kubernetes is never changed directly by the investigator or remediation path.

## Architecture

```mermaid
flowchart TD
    App[Instrumented Kubernetes workloads]
    OTel[OpenTelemetry Collector]
    Prom[Prometheus]
    Loki[Loki]
    Tempo[Tempo]
    AM[Alertmanager]
    API[incidentpilot-api]
    DB[(PostgreSQL)]
    Agent[incidentpilot-agent<br/>operator-triggered, one-shot]
    MCP[incidentpilot-mcp<br/>constrained read-only tools]
    K8s[Kubernetes read APIs]
    Argo[Argo CD and GitHub read APIs]
    LLM[Provider adapter<br/>Groq or Ollama]
    OPA[OPA / Rego]
    GitHub[GitHub pull request]
    GitOps[Human review → merge → Argo CD]

    App -->|OTLP metrics, traces, logs| OTel
    OTel --> Prom
    OTel --> Loki
    OTel --> Tempo
    Prom --> AM --> API
    API <--> DB
    Agent -->|load incident and save report| DB
    Agent -->|bounded evidence calls| MCP
    Agent -->|structured analysis| LLM
    MCP --> K8s
    MCP --> Prom
    MCP --> Loki
    MCP --> Tempo
    MCP --> Argo
    MCP -->|single request_remediation boundary| API
    API -->|trusted validation and risk derivation| OPA
    OPA -->|allowed only| GitHub --> GitOps
```

The critical boundary is between reasoning and authorization. Evidence, policy
input, patch generation, and GitHub execution are controlled by deterministic
code—not by the model.

## What is implemented

- Alert ingestion, deduplication, lifecycle persistence, and trace context.
- Bounded MCP reads for Kubernetes, Prometheus, Loki, Tempo, Argo CD, and
  GitHub change intelligence.
- Provider-agnostic LLM adapters for Groq and Ollama.
- Evidence persistence with provenance, source, resource reference, collection
  window, and normalized summary.
- Deterministic RCA verification for six reproducible demo faults:
  OOMKilled, image pull failure, invalid configuration, broken Service routing,
  CPU/latency regression, and downstream payment failure.
- OPA-governed remediation: the demo OOM repair can change only the exact
  memory-limit scalar through a reviewable GitHub PR. It never merges a PR.
- OpenTelemetry traces and metrics for alerts, investigations, LLM/MCP calls,
  policy decisions, and remediation outcomes.
- Helm-based external onboarding for a single existing namespace, with
  validated workload, pod-selector, alert, and read-only evidence scope.

The demo is a reproducible test fixture, not the product's only use case. An
external profile can investigate configured workloads, but its current generic
RCA support is intentionally narrower: Kubernetes OOM and image-pull evidence
can be verified; unsupported causes stop safely with `INSUFFICIENT_EVIDENCE`.

## Technology stack

| Area | Technology |
| --- | --- |
| Services | Go |
| Local Kubernetes | kind and kubectl |
| Telemetry | OpenTelemetry Collector, Prometheus, Loki, Tempo, Grafana |
| Alerting and storage | Alertmanager, PostgreSQL |
| Investigation boundary | Model Context Protocol (MCP) Go SDK |
| AI providers | Groq or Ollama through project-owned adapters |
| Authorization | OPA / Rego |
| Delivery | Helm, Argo CD, GitHub pull requests |
| Cloud assets | Terraform for optional AWS/EKS deployment |

## Quick start: test locally

### Prerequisites

- Go 1.27 or newer
- Docker
- Make
- kubectl
- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/)

Run the deterministic repository checks first:

```sh
make check
make eval
make infra-check
```

Start the fully local demo stack:

```sh
make kind-up
make kind-status
```

This creates or refreshes only the `kind-incidentpilot` cluster. It includes the
demo app, traffic generator, observability components, Alertmanager,
IncidentPilot API/MCP services, and PostgreSQL. No AWS resources are created.

To remove all local cluster data later:

```sh
make kind-down
```

### Run an end-to-end incident investigation

Create `.env` from `.env.example` if it does not already exist. To use model
analysis, add your own Groq or Ollama configuration; never commit provider keys.

In three terminals, create local-only forwards:

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/postgres 15432:5432
```

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/incidentpilot-mcp 18081:8081
```

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/incidentpilot-api 18080:8080
```

Inject and verify the safe local OOM fixture:

```sh
make scenario-oom-inject
make scenario-oom-check
```

After Alertmanager delivers the alert, get the newest incident ID:

```sh
curl -H 'Authorization: Bearer incidentpilot-local-dev-token-v1' \
  'http://127.0.0.1:18080/api/v1/incidents?limit=10'
```

Run the investigator with that ID:

```sh
set -a
source .env
set +a

./bin/incidentpilot-agent -incident-id INCIDENT_UUID
```

Expected result: `ROOT_CAUSE_FOUND` with cause `memory_limit_oom` and cited
pod/Deployment evidence. Always restore the fixture afterward:

```sh
make scenario-oom-reset
```

Run every reproducible fault scenario, including reset, with:

```sh
make eval-kind
```

Each [scenario README](scenarios/oom-memory-limit/README.md) describes its
fault injection, expected evidence, and reset command.

## Remediation and PR testing

The remediation path accepts a structured proposal only after a verified RCA.
For the local OOM scenario, OPA permits exactly the `48Mi` to `128Mi` memory
limit repair in `deploy/kind/30-demo.yaml`; broader changes are denied.

A real PR additionally requires a GitHub fine-grained token restricted to this
repository with Contents and Pull requests read/write, configured as the local
`incidentpilot-github-remediation` Secret. Without it, the expected safe result
is `PR_FAILED/github_not_configured`. The remediation request must identify the
verified incident/investigation, its exact cited evidence IDs, and the allowed
`48Mi` to `128Mi` memory-limit change.

## Use with an existing cluster

Do not copy the demo application into another cluster. Install the chart with an
external onboarding profile that declares the application namespace, workloads,
container names, pod selectors, and allowed alerts. IncidentPilot creates only
its own read-only RBAC binding in that application namespace; it does not read
Secrets, execute into pods, or modify workloads.

Start from [the external values example](deploy/helm/incidentpilot/values-external.example.yaml).
Use the same validated profile JSON in the API, MCP, and one-shot agent; the
chart passes it to the API and MCP automatically.

The base [values.yaml](deploy/helm/incidentpilot/values.yaml) intentionally uses
portable placeholders. For this repository's demo images and GitHub repository
defaults, layer [values-demo.example.yaml](deploy/helm/incidentpilot/values-demo.example.yaml)
on top. Neither file contains credentials.

## Current limits

- The agent is intentionally one-shot; alert-to-agent orchestration is not yet
  automated.
- Live GitHub PR creation requires an explicitly configured write credential and
  human review; IncidentPilot never merges it.
- The kind environment uses public disposable local credentials and ephemeral
  observability storage. It is for development only.
- Terraform and Helm assets support AWS/EKS deployment, but deployment is an
  operator-controlled action and this repository never runs `terraform apply`.
