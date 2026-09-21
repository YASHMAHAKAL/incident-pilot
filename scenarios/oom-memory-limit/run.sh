#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
deployment=deployment/payments-api

usage() {
  echo "usage: sh scenarios/oom-memory-limit/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then
  usage
fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

limit=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[?(@.name=="payments-api")].resources.limits.memory}')
working_set=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[?(@.name=="payments-api")].env[?(@.name=="DEMO_PAYMENT_WORKING_SET_MIB")].value}')
if [ "$1" != reset ] && [ "$working_set" != 72 ]; then
  echo "payments-api is not configured with the expected 72 MiB demo working set" >&2
  exit 1
fi

case "$1" in
  inject)
    if [ "$limit" = 48Mi ]; then
      echo "OOM scenario is already injected"
      exit 0
    fi
    if [ "$limit" != 128Mi ]; then
      echo "refusing to replace unexpected payments-api memory limit: $limit" >&2
      exit 1
    fi
    resource set resources "$deployment" -c payments-api --requests=cpu=25m,memory=32Mi --limits=memory=48Mi
    echo "injected memory limit regression: payments-api 128Mi -> 48Mi"
    ;;
  check)
    if [ "$limit" != 48Mi ]; then
      echo "OOM scenario is not injected; current limit is $limit" >&2
      exit 1
    fi
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      reasons=$(resource get pods -l app=payments-api -o 'jsonpath={range .items[*]}{range .status.containerStatuses[*]}{.lastState.terminated.reason}{"\n"}{end}{end}')
      if printf '%s\n' "$reasons" | grep -qx OOMKilled; then
        echo "observed OOMKilled in payments-api container status"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "payments-api did not report OOMKilled within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$limit" = 128Mi ]; then
      echo "OOM scenario is already reset"
      exit 0
    fi
    if [ "$limit" != 48Mi ]; then
      echo "refusing to replace unexpected payments-api memory limit: $limit" >&2
      exit 1
    fi
    resource set resources "$deployment" -c payments-api --requests=cpu=25m,memory=32Mi --limits=memory=128Mi
    resource rollout status "$deployment" --timeout=120s
    echo "restored payments-api memory limit to 128Mi"
    ;;
  *) usage ;;
esac
