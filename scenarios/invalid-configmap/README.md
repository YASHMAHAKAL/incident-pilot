# Application startup failure from an invalid ConfigMap

This fixture changes `orders-config.data.order_mode` from `normal` to `unsupported` in the dedicated `kind-incidentpilot` cluster, then restarts only the `orders-api` Deployment. The app validates the value at startup and exits with an explicit error. The new pod should enter `CrashLoopBackOff`; the old ready replica can keep serving during the failed rolling update.

```sh
make scenario-invalid-configmap-inject
make scenario-invalid-configmap-check
make scenario-invalid-configmap-reset
```

Run these from the repository root after `make kind-up`. Reset restores `normal`, restarts the Deployment, and waits for rollout. Always reset after injection, even if check fails. The `DemoInvalidOrderConfig` Prometheus rule automatically creates an orders-api incident from the pod's `CrashLoopBackOff` state. The [ground truth](ground-truth.yaml) identifies the ConfigMap, Deployment reference, failed pod, and startup log as evidence. The one-shot investigator requires all four cited records before trusted code emits an RCA. ConfigMap values consumed as environment variables require a pod restart to take effect.
