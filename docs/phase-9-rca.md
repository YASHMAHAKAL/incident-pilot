# Phase 9: evidence-backed RCA

Phase 9 expands the investigator from its original OOM-only exit criterion while preserving a fail-closed result. All six v1 scenarios now have a conventional alert path, bounded evidence collection, a deterministic verifier, and scenario ground truth. The one-shot agent can produce an auditable RCA only when the model names an allowlisted cause and cites every record required by trusted Go code.

The model may classify the cause as `deployment_references_unpullable_image`, but it cannot authorize that conclusion. Trusted Go code requires all three cited records to agree:

- the `orders-api` Deployment names a syntactically valid image and uses pull policy `Always`;
- an `orders-api` pod reports the same image in `ErrImagePull` or `ImagePullBackOff`;
- a Kubernetes `Failed` event for that exact pod says Kubernetes failed to pull that exact image.

When those checks pass, trusted code constructs the conclusion and attaches the three evidence IDs. Missing citations, mismatched images, unsupported causes, and model claims based only on logs remain `INSUFFICIENT_EVIDENCE`. Kubernetes metadata and event messages are projected into a bounded diagnostic brief and still treated as untrusted data.

For `unsupported_order_mode_from_configmap`, trusted code instead requires four cited records to agree:

- the fixed `orders-config.data.order_mode` projection equals `unsupported`;
- the orders-api Deployment projection shows `DEMO_ORDER_MODE` consumes `orders-config.data.order_mode`;
- an observed orders-api pod is in `CrashLoopBackOff`;
- a bounded log tail from that exact pod reports invalid order mode `unsupported`.

The MCP identity can only `get` the named `orders-config` ConfigMap, and the tool returns only the accepted `order_mode` field. Pod-log collection is selected by trusted code from the already observed crashing pod. Neither resource name nor pod name comes from the model.

For `service_selector_matches_no_payment_pods`, trusted code requires four cited records:

- the payments-api Service selector is `app=payments-api-disconnected`;
- at least one payments-api EndpointSlice exists and all its endpoints have zero addresses;
- a running, ready payments-api container remains labeled `app=payments-api`;
- the incident-window frontend error-rate series contains a positive sample.

This proves the payment workload is healthy while the Service routing layer selects no endpoints and checkout requests fail. EndpointSlice queries derive their label selector from an allowlisted workload; the model cannot supply a namespace or raw selector.

For `excessive_cpu_work_per_order`, the dedicated `DemoCheckoutLatency` rule detects successful frontend requests whose fixed 30-second average duration exceeds 750 ms. Trusted code requires four cited records:

- the orders-api Deployment projection shows `DEMO_ORDER_CPU_BURN_MS=1000`;
- the frontend `success_latency_avg` series contains a sample of at least 750 ms, proving checkout remained successful but slow;
- the orders-api `success_latency_avg` series contains a sample of at least 900 ms;
- one trace contains HTTP 200 frontend and orders-api server spans lasting at least 900 ms.

The MCP Deployment response continues to strip arbitrary environment variables. It adds only the exact CPU scenario variable when its value is in the accepted set (`0` or `1000`). The Prometheus query is a fixed sum/count expression; neither the model nor the caller can provide PromQL.

For `payment_processor_failure_mode_enabled`, the existing checkout-error signal opens a frontend incident. Trusted code requires three cited records:

- the payments-api Deployment projection shows `DEMO_PAYMENT_MODE=fail`;
- the incident-window frontend error-rate series contains a positive sample;
- one distributed trace contains a payments-api server span with HTTP 503 and error status, plus orders-api and frontend server spans with HTTP 502 and error status.

The trace requirement proves both the downstream origin and propagation in one trace rather than inferring the cause from frontend symptoms. Raw traces remain persisted evidence, while the model receives at most 24 projected spans containing only service, span name/kind, duration, HTTP status, and span status. Arbitrary trace attributes are excluded.

## Local evaluation

Start the stack, inject a fixture, and wait for its state. For example:

```sh
make kind-up
make scenario-bad-image-inject
make scenario-bad-image-check
```

Allow roughly one minute for Prometheus evaluation and Alertmanager delivery. Retrieve the fresh `DemoImagePullBackOff` incident through the incident API, forward PostgreSQL and MCP as described in the [investigator guide](phase-7-investigator.md), load `.env`, and run:

```sh
./bin/incidentpilot-agent -incident-id INCIDENT_UUID
```

The expected bad-image outcome is `ROOT_CAUSE_FOUND` with component `orders-api` and three Kubernetes evidence IDs. The CPU fixture creates `DemoCheckoutLatency`; the downstream failure fixture creates `DemoCheckoutErrors`. Their expected causes are `excessive_cpu_work_per_order` and `payment_processor_failure_mode_enabled`. Always restore the selected fixture:

```sh
make scenario-bad-image-reset
```

Phase 9 does not automatically launch the one-shot investigator when an alert arrives, and no provider call is part of `make check`. Scenario scripts mutate only the disposable local evaluation cluster and are not exposed to the investigator. Automatic orchestration and policy-governed remediation begin after Phase 9.

## Manual bad-image evaluation (2026-09-21)

A live run against a fresh `DemoImagePullBackOff` incident using Groq `openai/gpt-oss-20b` returned `ROOT_CAUSE_FOUND`. The trusted verifier cited the matching orders-api Deployment, waiting pod, and failed-pull event. Investigation `14f408ff-fd9a-48ca-bc18-e3437694ecf7` used one LLM call, zero retries, nine tool observations, 2,027 input tokens, and 402 output tokens. Reading the report back by ID confirmed PostgreSQL persistence. The fixture was reset and incident `05872ec6-c3f8-4f6b-9602-1726fb87c96f` transitioned to `RESOLVED`. This is one integration smoke run, not a reliability measurement.

## Local deterministic validation (2026-09-21)

The CPU fixture fired `DemoCheckoutLatency` at approximately 1.001 seconds and created incident `1a2477d9-6aa5-422f-b2d3-3fa7323e52a7`. Bounded collection persisted the `success_latency_avg` range and selected a trace whose frontend and orders-api server spans both returned HTTP 200 and lasted 1,000 ms. During the fault, MCP projected only `DEMO_ORDER_CPU_BURN_MS=1000` from the orders Deployment.

The downstream fixture fired `DemoCheckoutErrors` and created incident `11538f20-7ef6-45bf-9afd-d5443c855402`. Bounded collection selected one trace containing payments HTTP 503 and orders/frontend HTTP 502 with error status, and MCP projected only `DEMO_PAYMENT_MODE=fail` from the payments Deployment. Both fixtures were reset and both incidents transitioned to `RESOLVED`. These checks exercised deterministic detection, collection, persistence, filtering, and verifier predicates without sending evidence to an LLM; they are integration smoke tests, not model-quality measurements.
