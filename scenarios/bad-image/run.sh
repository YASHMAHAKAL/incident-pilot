#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
deployment=deployment/orders-api
baseline_image=incidentpilot-demo:local
fault_image=127.0.0.1:59999/incidentpilot/missing:phase2-bad-image

usage() {
  echo "usage: sh scenarios/bad-image/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then
  usage
fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

container_name=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[0].name}')
image=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[0].image}')
pull_policy=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[0].imagePullPolicy}')
if [ "$container_name" != orders-api ]; then
  echo "refusing to modify unexpected first container: $container_name" >&2
  exit 1
fi

case "$1" in
  inject)
    if [ "$image" = "$fault_image" ] && [ "$pull_policy" = Always ]; then
      echo "bad-image scenario is already injected"
      exit 0
    fi
    if [ "$image" != "$baseline_image" ] || [ "$pull_policy" != Never ]; then
      echo "refusing to replace unexpected orders-api image or pull policy: $image $pull_policy" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"orders-api"},{"op":"test","path":"/spec/template/spec/containers/0/image","value":"incidentpilot-demo:local"},{"op":"test","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Never"},{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"127.0.0.1:59999/incidentpilot/missing:phase2-bad-image"},{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Always"}]'
    echo "injected bad image on orders-api; waiting for the new pod to fail pulling"
    ;;
  check)
    if [ "$image" != "$fault_image" ] || [ "$pull_policy" != Always ]; then
      echo "bad-image scenario is not injected; current image and pull policy: $image $pull_policy" >&2
      exit 1
    fi
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      statuses=$(resource get pods -l app=orders-api -o 'jsonpath={range .items[*]}{.spec.containers[?(@.name=="orders-api")].image}{" "}{.status.containerStatuses[?(@.name=="orders-api")].state.waiting.reason}{"\n"}{end}')
      if printf '%s\n' "$statuses" | grep -Fxq "$fault_image ImagePullBackOff"; then
        echo "observed ImagePullBackOff on the new orders-api pod"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "new orders-api pod did not report ImagePullBackOff within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$image" = "$baseline_image" ] && [ "$pull_policy" = Never ]; then
      echo "bad-image scenario is already reset"
      exit 0
    fi
    if [ "$image" != "$fault_image" ] || [ "$pull_policy" != Always ]; then
      echo "refusing to replace unexpected orders-api image or pull policy: $image $pull_policy" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"orders-api"},{"op":"test","path":"/spec/template/spec/containers/0/image","value":"127.0.0.1:59999/incidentpilot/missing:phase2-bad-image"},{"op":"test","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Always"},{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"incidentpilot-demo:local"},{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Never"}]'
    resource rollout status "$deployment" --timeout=120s
    echo "restored orders-api image and pull policy"
    ;;
  *) usage ;;
esac
