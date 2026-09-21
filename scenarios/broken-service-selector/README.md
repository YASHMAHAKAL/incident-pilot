# Traffic failure from a broken Service selector

This fixture changes the `payments-api` Service's `app` selector in the dedicated `kind-incidentpilot` cluster. The new value matches no payment pods, so the Service loses its endpoints while the Deployment remains healthy. It also restarts `orders-api` to discard any existing keep-alive connection to payments; new requests then fail and `/checkout` returns HTTP 502.

```sh
make scenario-broken-selector-inject
make scenario-broken-selector-check
make scenario-broken-selector-reset
```

Run these from the repository root after `make kind-up`. Check waits for an empty payments EndpointSlice and checkout failures in the traffic-generator log. Reset restores the original selector and waits for endpoints and successful checkouts. Always reset after injection. The [ground truth](ground-truth.yaml) distinguishes this network routing failure from a crashing payment pod.
