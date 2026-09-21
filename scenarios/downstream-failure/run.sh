#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
deployment=deployment/payments-api

usage() {
  echo "usage: sh scenarios/downstream-failure/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then usage; fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

mode=$(resource get "$deployment" -o 'jsonpath={.spec.template.spec.containers[?(@.name=="payments-api")].env[?(@.name=="DEMO_PAYMENT_MODE")].value}')

case "$1" in
  inject)
    if [ "$mode" = fail ]; then
      echo "downstream-failure scenario is already injected"
      exit 0
    fi
    if [ "$mode" != normal ]; then
      echo "refusing to replace unexpected payment mode: $mode" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"payments-api"},{"op":"test","path":"/spec/template/spec/containers/0/env/2/name","value":"DEMO_PAYMENT_MODE"},{"op":"test","path":"/spec/template/spec/containers/0/env/2/value","value":"normal"},{"op":"replace","path":"/spec/template/spec/containers/0/env/2/value","value":"fail"}]'
    resource rollout status "$deployment" --timeout=120s
    echo "injected payment processor failure"
    ;;
  check)
    if [ "$mode" != fail ]; then
      echo "downstream-failure scenario is not injected; mode is $mode" >&2
      exit 1
    fi
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      if resource logs "$deployment" --since=30s --tail=30 | grep -Fq '"outcome":"downstream_failure"' &&
         resource logs deployment/traffic-generator --since=30s --tail=30 | grep -Fq '"status":502'; then
        echo "observed payment failure and checkout HTTP 502"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "payment failure and checkout HTTP 502 not observed within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$mode" = normal ]; then
      echo "downstream-failure scenario is already reset"
      exit 0
    fi
    if [ "$mode" != fail ]; then
      echo "refusing to replace unexpected payment mode: $mode" >&2
      exit 1
    fi
    resource patch "$deployment" --type=json -p '[{"op":"test","path":"/spec/template/spec/containers/0/name","value":"payments-api"},{"op":"test","path":"/spec/template/spec/containers/0/env/2/name","value":"DEMO_PAYMENT_MODE"},{"op":"test","path":"/spec/template/spec/containers/0/env/2/value","value":"fail"},{"op":"replace","path":"/spec/template/spec/containers/0/env/2/value","value":"normal"}]'
    resource rollout status "$deployment" --timeout=120s
    echo "restored normal payment behavior"
    ;;
  *) usage ;;
esac
