#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
deployment=deployment/orders-api

usage() {
  echo "usage: sh scenarios/cpu-latency/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then usage; fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

duration=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[?(@.name=="orders-api")].env[?(@.name=="DEMO_ORDER_CPU_BURN_MS")].value}')

case "$1" in
  inject)
    if [ "$duration" = 1000 ]; then
      echo "CPU-latency scenario is already injected"
      exit 0
    fi
    if [ "$duration" != 0 ]; then
      echo "refusing to replace unexpected order CPU burn duration: $duration" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"orders-api"},{"op":"test","path":"/spec/template/spec/containers/0/env/3/name","value":"DEMO_ORDER_CPU_BURN_MS"},{"op":"test","path":"/spec/template/spec/containers/0/env/3/value","value":"0"},{"op":"replace","path":"/spec/template/spec/containers/0/env/3/value","value":"1000"}]'
    resource rollout status "$deployment" --timeout=120s
    echo "injected one second of order CPU work per request"
    ;;
  check)
    if [ "$duration" != 1000 ]; then
      echo "CPU-latency scenario is not injected; duration is $duration" >&2
      exit 1
    fi
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      if resource logs "$deployment" --since=30s --tail=30 | awk '
        /"service":"orders-api"/ && /"outcome":"success"/ {
          if (match($0, /"duration_ms":[0-9]+/)) {
            value = substr($0, RSTART + 14, RLENGTH - 14)
            if (value + 0 >= 900) found = 1
          }
        }
        END {exit !found}
      '; then
        echo "observed successful orders-api request taking at least 900 ms"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "orders-api did not report the expected latency within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$duration" = 0 ]; then
      echo "CPU-latency scenario is already reset"
      exit 0
    fi
    if [ "$duration" != 1000 ]; then
      echo "refusing to replace unexpected order CPU burn duration: $duration" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"orders-api"},{"op":"test","path":"/spec/template/spec/containers/0/env/3/name","value":"DEMO_ORDER_CPU_BURN_MS"},{"op":"test","path":"/spec/template/spec/containers/0/env/3/value","value":"1000"},{"op":"replace","path":"/spec/template/spec/containers/0/env/3/value","value":"0"}]'
    resource rollout status "$deployment" --timeout=120s
    echo "restored zero order CPU burn"
    ;;
  *) usage ;;
esac
