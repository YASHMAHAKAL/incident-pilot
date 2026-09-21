# OOMKilled from a memory-limit regression

This scenario runs only against the dedicated `kind-incidentpilot` cluster and the `incidentpilot-demo` namespace. It changes only the `payments-api` Deployment's container memory limit. The demo service retains a bounded 72 MiB working set under ordinary traffic. Its baseline 128 MiB limit supports that load; the injected 48 MiB limit does not.

From the repository root, after `make kind-up`:

```sh
make scenario-oom-inject
make scenario-oom-check
make scenario-oom-reset
```

`scenario-oom-check` waits up to two minutes for Kubernetes to report `OOMKilled` in the container's previous termination state. Reset restores the 128 MiB limit and waits for the Deployment rollout. Always run reset after an injection, including when the check fails. Injection checks the expected workload settings; reset refuses to replace an unexpected memory limit.

The [ground truth](ground-truth.yaml) records the expected root cause and evidence. During the fault, inspect the Deployment memory limit, `payments-api` pod status/events, `demo_process_heap_alloc_bytes{service_name="payments-api"}` in Prometheus, and upstream errors in the frontend/orders metrics and traces. A future IncidentPilot investigator must discover these signals through read-only tools; the injection command is a local test fixture, not an investigator capability.
