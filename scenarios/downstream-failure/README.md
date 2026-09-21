# Downstream payment failure visible through tracing

This fixture changes only `DEMO_PAYMENT_MODE` on the `payments-api` Deployment in the dedicated `kind-incidentpilot` cluster. With `fail`, payments returns HTTP 503, orders and frontend return HTTP 502, and the propagated trace links all three services. The payment span records the originating error, making the downstream cause distinguishable from an upstream symptom.

```sh
make scenario-downstream-failure-inject
make scenario-downstream-failure-check
make scenario-downstream-failure-reset
```

Run these from the repository root after `make kind-up`. Check waits for payment-failure logs and failed checkouts. `DemoCheckoutErrors` creates a frontend incident. The one-shot investigator requires the payments Deployment failure mode, a positive frontend error rate, and one distributed trace containing the originating payment HTTP 503 plus propagated orders and frontend HTTP 502 errors before trusted code emits `payment_processor_failure_mode_enabled`. Reset restores normal payment behavior and waits for rollout. Always reset after injection. The [ground truth](ground-truth.yaml) records the expected cause and trace evidence.
