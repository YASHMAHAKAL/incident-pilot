# ImagePullBackOff from a bad image

This scenario runs only against the dedicated `kind-incidentpilot` cluster and the `incidentpilot-demo` namespace. It changes only the `orders-api` Deployment's image and pull policy. The injected image points at an unreachable registry on the kind node's loopback address. The pull policy changes from `Never` to `Always` so Kubernetes actually attempts the pull; changing the image alone would yield `ErrImageNeverPull` instead.

From the repository root, after `make kind-up`:

```sh
make scenario-bad-image-inject
make scenario-bad-image-check
make scenario-bad-image-reset
```

`scenario-bad-image-check` waits up to two minutes for the new pod's container to report `ImagePullBackOff`. The Deployment's default rolling-update strategy retains the old, ready replica while the new replica cannot start, so this fixture demonstrates a stalled rollout rather than guaranteed checkout failures. kube-state-metrics exposes the pod waiting reason, and the `DemoImagePullBackOff` Prometheus rule automatically creates an orders-api incident through Alertmanager. Reset restores the original image and `Never` policy and waits for a healthy rollout. Always reset after injection, including when check fails. Injection and reset refuse to overwrite unexpected image settings.

The [ground truth](ground-truth.yaml) records the expected root cause and evidence. The one-shot investigator obtains that evidence through read-only tools and emits an RCA only when its final analysis cites the matching Deployment image/pull policy, waiting pod, and failed-pull event. The injection command remains only a local evaluation fixture; the investigator cannot invoke it or mutate the cluster.
