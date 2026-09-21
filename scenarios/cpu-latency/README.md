# CPU work and latency regression

This fixture changes only `DEMO_ORDER_CPU_BURN_MS` on the `orders-api` Deployment in the dedicated `kind-incidentpilot` cluster. Each order performs one second of bounded CPU work before calling payments. Checkout stays successful but its order span and request duration become much longer. The default is zero; the injected value is `1000` milliseconds.

```sh
make scenario-cpu-latency-inject
make scenario-cpu-latency-check
make scenario-cpu-latency-reset
```

Run these from the repository root after `make kind-up`. Check waits for a successful order request logged with at least 900 ms duration. `demo_request_duration_seconds` and the order span provide telemetry evidence. Reset restores zero work and waits for rollout. Always reset after injection. The [ground truth](ground-truth.yaml) describes the intended evidence and cause.
