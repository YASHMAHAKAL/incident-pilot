# Phase 3: alert-driven incidents

The local signal paths are:

```text
demo checkout error metric
-> Prometheus DemoCheckoutErrors rule
-> Alertmanager webhook
-> incidentpilot-api
-> PostgreSQL incident

orders-api pod waiting state
-> kube-state-metrics
-> Prometheus DemoImagePullBackOff rule
-> Alertmanager webhook
-> incidentpilot-api
-> PostgreSQL incident

orders-api CrashLoopBackOff state
-> kube-state-metrics
-> Prometheus DemoInvalidOrderConfig rule
-> Alertmanager webhook
-> incidentpilot-api
-> PostgreSQL incident

successful frontend request duration
-> Prometheus DemoCheckoutLatency rule
-> Alertmanager webhook
-> incidentpilot-api
-> PostgreSQL incident
```

`make kind-up` deploys these paths to the dedicated `kind-incidentpilot` cluster. Prometheus scrapes every 10 seconds and evaluates rules every 10 seconds. The rules detect sustained frontend upstream errors, elevated successful checkout latency, orders-api image-pull failures, and orders-api startup crash loops. Together these signals cover all six Phase 2 fixtures; several failure modes intentionally share the checkout-error signal and are separated during investigation. Alertmanager delivers firing and resolved notifications. The API stores the validated alert identity and status, not the raw annotations or webhook body. It deduplicates retries by `(fingerprint, started_at)`; a replayed firing notification cannot reopen a resolved incident. A new alert episode with a new start time gets a new incident. PostgreSQL uses a local 1 GiB PVC, so records survive API and PostgreSQL pod restarts but are removed with the kind cluster.

To reproduce the end-to-end path after `make kind-up`:

```sh
kubectl --context kind-incidentpilot -n incidentpilot-system port-forward svc/incidentpilot-api 8080:8080
# In another terminal:
make scenario-broken-selector-inject
make scenario-broken-selector-check
curl -H 'Authorization: Bearer incidentpilot-local-dev-token-v1' http://127.0.0.1:8080/api/v1/incidents
make scenario-broken-selector-reset
```

Allow roughly a minute for alert evaluation and webhook delivery. The incident should first be `DETECTED`, then become `RESOLVED` after the fault clears and the alert resolves. Always reset the fault even if the check fails. `GET /api/v1/incidents?limit=20` lists the newest records (maximum limit 100); `GET /api/v1/incidents/{id}` retrieves one. `/healthz` is process liveness; `/readyz` checks PostgreSQL. The incident endpoints and webhook require the configured bearer token.

The checked-in token and PostgreSQL password in `deploy/kind/40-incident.yaml` are deliberately public, dummy values for a disposable local cluster. Do not reuse this manifest or its credentials outside kind. For another environment, provision separate non-committed credentials, TLS, network policy, backup/retention, and stronger API authentication. The API accepts `INCIDENTPILOT_DATABASE_URL` and `INCIDENTPILOT_WEBHOOK_TOKEN` together; without them, only `/healthz` is useful. The webhook has a 256 KiB body limit, a 50-alert batch limit, and narrow label validation. It does not expose mutation, shell, Kubernetes, or investigation tools.

Phase 3 only creates and updates incidents. It does not yet gather evidence, determine root cause, or propose remediation. Those capabilities start in later phases.
