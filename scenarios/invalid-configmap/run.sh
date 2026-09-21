#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
configmap=configmap/orders-config
deployment=deployment/orders-api

usage() {
  echo "usage: sh scenarios/invalid-configmap/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then usage; fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

mode=$(resource get "$configmap" -o 'jsonpath={.data.order_mode}')
source=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[?(@.name=="orders-api")].env[?(@.name=="DEMO_ORDER_MODE")].valueFrom.configMapKeyRef.name}')
if [ "$source" != orders-config ]; then
  echo "orders-api is not using the expected orders-config ConfigMap" >&2
  exit 1
fi

case "$1" in
  inject)
    if [ "$mode" = unsupported ]; then
      echo "invalid-ConfigMap scenario is already injected"
      exit 0
    fi
    if [ "$mode" != normal ]; then
      echo "refusing to replace unexpected order mode: $mode" >&2
      exit 1
    fi
    resource patch "$configmap" --type=json -p '[{"op":"test","path":"/data/order_mode","value":"normal"},{"op":"replace","path":"/data/order_mode","value":"unsupported"}]'
    resource rollout restart "$deployment"
    echo "injected unsupported order mode and restarted orders-api"
    ;;
  check)
    if [ "$mode" != unsupported ]; then
      echo "invalid-ConfigMap scenario is not injected; order mode is $mode" >&2
      exit 1
    fi
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      pod=$(resource get pods -l app=orders-api -o 'jsonpath={range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[?(@.name=="orders-api")].state.waiting.reason}{"\n"}{end}' | awk '$2 == "CrashLoopBackOff" {print $1; exit}')
      if [ -n "$pod" ] && resource logs "$pod" -c orders-api --tail=20 | grep -Fq 'invalid order mode'; then
        echo "observed orders-api CrashLoopBackOff and invalid order mode log"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "orders-api did not fail with the expected invalid mode within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$mode" = normal ]; then
      echo "invalid-ConfigMap scenario is already reset"
      exit 0
    fi
    if [ "$mode" != unsupported ]; then
      echo "refusing to replace unexpected order mode: $mode" >&2
      exit 1
    fi
    resource patch "$configmap" --type=json -p '[{"op":"test","path":"/data/order_mode","value":"unsupported"},{"op":"replace","path":"/data/order_mode","value":"normal"}]'
    resource rollout restart "$deployment"
    resource rollout status "$deployment" --timeout=120s
    echo "restored normal order mode and healthy orders-api rollout"
    ;;
  *) usage ;;
esac
