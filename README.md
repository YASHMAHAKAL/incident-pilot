# IncidentPilot

IncidentPilot is an evidence-driven Kubernetes incident investigator. The [project roadmap](.agents/skills/incidentpilot/references/roadmap.md) defines the staged build. This repository implements the complete v1 roadmap through [Phase 14 AWS/GitOps deployment assets](docs/phase-14-aws.md), including the [Phase 13 reproducible safety/RCA evaluation](docs/phase-13-evaluation.md). Automatic investigation orchestration remains future work.

## Local workflow

Requirements: Go 1.27 or newer, Make, and Docker for container builds. The Kubernetes demo also needs [kind](https://kind.sigs.k8s.io/docs/user/quick-start/) and kubectl. The `kind` executable must be on `PATH` (or passed as `KIND=/path/to/kind`).

The Go module currently uses the local path `incidentpilot`. Replace it with the eventual repository import path when a Git remote is chosen.

```sh
make check
make eval
make run
```

The API listens on `:8080` by default. `GET /healthz` returns `ok`; it only reports that the process can serve HTTP. Override the bind address with `INCIDENTPILOT_HTTP_ADDR` (for example, `127.0.0.1:8080`). Set `INCIDENTPILOT_SHUTDOWN_TIMEOUT` to a positive Go duration such as `15s`; the default is `10s`. Running without a database and webhook token leaves only liveness enabled; see the [Phase 3 guide](docs/phase-3-incidents.md) for the incident path.

Build the local images with `make container-build`. They run as non-root users and contain only statically linked binaries.

## Observable demo

The demo graph is `frontend -> orders-api -> payments-api`. Run `make kind-up` to create a dedicated `incidentpilot` kind cluster, build and load the demo images, and apply the manifests. All Kubernetes commands in the Makefile target the `kind-incidentpilot` context explicitly. `make kind-status` lists the pods. The traffic generator calls `/checkout` every two seconds.

Re-running `make kind-up` reloads local images and restarts the demo, API, MCP, Alertmanager, and observability pods. Prometheus, Loki, Tempo, and Alertmanager keep ephemeral local data, so restarts clear their data. PostgreSQL uses a PVC and retains incidents across pod restarts. To remove this dedicated cluster and its data when you are finished, run `make kind-down`.

Open Grafana locally with:

```sh
kubectl --context kind-incidentpilot -n incidentpilot-observability port-forward svc/grafana 3000:3000
```

Visit `http://localhost:3000` and use Explore. Query `demo_requests_total` in Prometheus, `{service_name="frontend"}` in Loki, and search `service.name=frontend` in Tempo. A checkout trace includes frontend, orders-api, and payments-api spans. You can also port-forward `svc/frontend` in `incidentpilot-demo` on port 8080 and open `/checkout` yourself. The observability stores use ephemeral local filesystem data, and Grafana allows anonymous viewing inside the local cluster; this deployment is for development only.

Grafana also provisions an **IncidentPilot control plane** dashboard for alert, investigation, LLM, MCP, policy, and remediation metrics. Incident and investigation API responses include a trace ID when telemetry is enabled; use it to locate the corresponding control-plane trace in Tempo.

The telemetry route is: app OTLP HTTP -> Collector -> Prometheus exporter or Tempo; Kubernetes container logs -> Collector filelog receiver -> Loki OTLP endpoint. Collector configuration follows the [container log parser](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/pkg/stanza/docs/operators/container.md) and [Loki OTLP ingestion](https://grafana.com/docs/loki/latest/send-data/otel/otel-collector-getting-started/) guidance.

## Current scope

The API receives Alertmanager webhooks, deduplicates incidents, and persists them in PostgreSQL; see the [Phase 3 guide](docs/phase-3-incidents.md). The separate MCP service can query bounded Kubernetes and observability data; see the [Phase 4 guide](docs/phase-4-mcp.md). Phase 5 can deterministically collect, normalize, and persist evidence for a recent demo incident; see the [evidence guide](docs/phase-5-evidence.md). Phase 6 provides interchangeable Groq and Ollama adapters; see the [provider guide](docs/phase-6-llm.md). Phase 7 adds a one-shot investigator that performs bounded deterministic evidence traversal followed by one structured LLM analysis call and persists the verified report; see the [investigator guide](docs/phase-7-investigator.md). Phase 8 can correlate an incident window with an Argo CD deployment and the corresponding bounded GitHub commit/diff when those optional sources are configured. Phase 9 verifies evidence-backed RCAs for all six v1 fixtures: [OOMKilled](scenarios/oom-memory-limit/README.md), [ImagePullBackOff](scenarios/bad-image/README.md), [invalid ConfigMap](scenarios/invalid-configmap/README.md), [broken Service selector](scenarios/broken-service-selector/README.md), [CPU/latency regression](scenarios/cpu-latency/README.md), and [downstream failure](scenarios/downstream-failure/README.md). Phase 10 accepts one evidence-linked remediation proposal, derives risk in trusted code, and applies embedded OPA/Rego policy. Phase 11 keeps execution inside that same boundary: an allowed OOM repair can generate one exact manifest edit, commit it to a deterministic branch, open or reuse a reviewable GitHub PR, and durably audit the result. It cannot merge PRs or mutate Kubernetes. Automatic alert-to-agent orchestration is not implemented. The demo uses a simulated payment response and does not persist orders.

The AWS deployment is infrastructure-as-code only until an operator explicitly supplies credentials, reviews cost, publishes immutable images, runs Terraform, supplies runtime secrets, and bootstraps Argo CD. See the [Phase 14 runbook](docs/phase-14-aws.md). Repository automation never runs `terraform apply` or merges remediation PRs.
