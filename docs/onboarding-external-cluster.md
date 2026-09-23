# Onboard an existing application

IncidentPilot can scope its read-only investigation plane to one existing
namespace and up to 20 explicitly named Deployments. Start with
[values-external.example.yaml](../deploy/helm/incidentpilot/values-external.example.yaml).
The target namespace must already exist; the chart does not install or replace
your application. For each Deployment, set its container name and a pod label
that identifies its pods. Each Alertmanager alert must have `namespace`, `service`,
`alertname`, and `severity` labels. `service` must equal a configured workload
`name`, and `alertname` must appear in `alerts` for that workload.

The chart passes the validated profile to the API and MCP server. The API
rejects alerts outside the profile. MCP constructs fixed Kubernetes reads in
the configured namespace, selects pods with the configured label, and reads
bounded Loki logs with both service and namespace labels. Trace reads require
a verified trace-to-namespace boundary and are disabled for this profile. It
cannot read Secrets, execute in pods, or mutate workloads. Prometheus' current
metric expressions are specific to the demo, so external investigations omit
them. The Loki endpoint is configurable through `runtime.lokiURL`; an
unavailable source appears as a collection failure.

Set `demo.enabled: false`, keep `runtime.environment: production`, and provide
the normal runtime Secret before deploying. Validate the chart with:

```sh
helm lint deploy/helm/incidentpilot -f deploy/helm/incidentpilot/values-external.example.yaml
helm template incidentpilot deploy/helm/incidentpilot --namespace incidentpilot-system -f deploy/helm/incidentpilot/values-external.example.yaml
```

The one-shot agent must receive the same JSON profile in
`INCIDENTPILOT_ONBOARDING_PROFILE`, alongside its usual database and MCP
settings. For example, with one workload:

```sh
export INCIDENTPILOT_ONBOARDING_PROFILE='{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app.kubernetes.io/name","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}],"verifiers":["kubernetes_oom","image_pull"]}'
./bin/incidentpilot-agent -incident-id INCIDENT_UUID
```

The API, MCP server, and agent must use the same profile. The optional
`kubernetes_oom` and `image_pull` verifiers can confirm those causes for a
configured workload from matching, recent Kubernetes evidence and persist a
`ROOT_CAUSE_FOUND` report. Unmatched causes produce `INSUFFICIENT_EVIDENCE`;
omitting `verifiers` keeps the profile evidence-only. External mode makes no
LLM call or remediation request. The other demo RCA patterns and their
remediation policies do not automatically apply to external workloads.
