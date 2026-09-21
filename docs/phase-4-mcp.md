# Phase 4: read-only MCP investigation surface

`incidentpilot-mcp` runs separately from the incident API. It serves stateless Streamable HTTP at `/mcp` on port 8081 using the official Go MCP SDK. A bearer token is required. The checked-in token and Secret are deliberately public, **kind-only** credentials; this manifest is not a production security configuration. `/healthz` is unauthenticated for probes.

The fourteen tools are `kubernetes_get_deployment`, `kubernetes_get_service`, `kubernetes_get_pods`, `kubernetes_get_events`, `kubernetes_get_pod_logs`, `prometheus_get_demo_metric`, `prometheus_get_demo_metric_range`, `loki_get_demo_logs`, `tempo_search_demo_traces`, `tempo_get_trace`, `argocd_get_application`, `argocd_get_revision_history`, `github_get_commit`, and `github_get_diff`. Inputs are limited to the three demo workloads, bounded time windows and counts, fixed metric selectors, validated trace IDs, or Git commit IDs found in bounded Argo CD history. Repository, application, project, and upstream URLs come only from MCP-server configuration. No tool accepts a namespace, URL, repository, branch, raw query language, shell command, secret name, or mutation request. Backend GETs have an eight-second deadline and response-size caps. Kubernetes and change-source responses are projected to diagnostic fields and scrubbed of credential-bearing text; all returned content remains **untrusted data**. Results include source and collection time.

The MCP ServiceAccount has a RoleBinding only in `incidentpilot-demo`: GET deployments/services and GET/LIST pods, pod logs, and events. It has no permission to read Secrets, use pod exec, or mutate workloads. This is an independent enforcement layer in addition to tool validation. Calls emit OTel spans, a bounded-label `incidentpilot_tool_calls_total` counter, and structured audit logs with tool/status/duration (not payloads).

To connect a client locally:

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/incidentpilot-mcp 18081:8081
```

Use Streamable HTTP URL `http://127.0.0.1:18081/mcp` with `Authorization: Bearer incidentpilot-mcp-local-dev-token-v1`. The token is for this disposable local cluster only. The next phase will turn tool responses into bounded, persisted evidence with provenance; Phase 4 does not claim an automated RCA.

The kind stack pins Loki 3.6.15. In this single-binary setup, both 3.7.0 and 3.7.8 reported `Ingester is shutting down` and rejected direct writes; 3.6.15 accepted writes and the MCP log tool returned live frontend entries. The local observability stores remain ephemeral.
