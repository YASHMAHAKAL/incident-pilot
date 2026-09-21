# ImagePullBackOff from a bad image

This scenario runs only against the dedicated `kind-incidentpilot` cluster and the `incidentpilot-demo` namespace. It changes only the `orders-api` Deployment's image and pull policy. The injected image points at an unreachable registry on the kind node's loopback address. The pull policy changes from `Never` to `Always` so Kubernetes actually attempts the pull; changing the image alone would yield `ErrImageNeverPull` instead.

From the repository root, after `make kind-up`:

```sh
make scenario-bad-image-inject
make scenario-bad-image-check
make scenario-bad-image-reset
```

`scenario-bad-image-check` waits up to two minutes for the new pod's container to report `ImagePullBackOff`. The Deployment's default rolling-update strategy retains the old, ready replica while the new replica cannot start, so this fixture demonstrates a stalled rollout rather than guaranteed checkout failures. Inspect the Deployment image/pull policy, the new pod's waiting reason, and its image-pull Events. Reset restores the original image and `Never` policy and waits for a healthy rollout. Always reset after injection, including when check fails. Injection and reset refuse to overwrite unexpected image settings.

The [ground truth](ground-truth.yaml) records the expected root cause and evidence. A future IncidentPilot investigator must obtain this evidence through read-only tools; the injection command is only a local evaluation fixture.
