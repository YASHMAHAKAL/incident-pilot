#!/bin/sh
set -eu

kubectl_bin=${KUBECTL:-kubectl}
context=kind-incidentpilot
namespace=incidentpilot-demo
service=service/payments-api
baseline_selector=payments-api
fault_selector=payments-api-disconnected

usage() {
  echo "usage: sh scenarios/broken-service-selector/run.sh inject|check|reset" >&2
  exit 2
}

if [ "$#" -ne 1 ]; then usage; fi

resource() {
  "$kubectl_bin" --context "$context" -n "$namespace" "$@"
}

selector=$(resource get "$service" -o 'jsonpath={.spec.selector.app}')

endpoint_addresses() {
  resource get endpointslices -l kubernetes.io/service-name=payments-api -o 'jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{"\n"}{end}{end}'
}

case "$1" in
  inject)
    if [ "$selector" = "$fault_selector" ]; then
      echo "broken-selector scenario is already injected"
      exit 0
    fi
    if [ "$selector" != "$baseline_selector" ]; then
      echo "refusing to replace unexpected payments-api selector: $selector" >&2
      exit 1
    fi
    resource patch "$service" --type=json -p '[{"op":"test","path":"/spec/selector/app","value":"payments-api"},{"op":"replace","path":"/spec/selector/app","value":"payments-api-disconnected"}]'
    resource rollout restart deployment/orders-api
    resource rollout status deployment/orders-api --timeout=120s
    echo "injected payments-api Service selector with no matching pods"
    ;;
  check)
    if [ "$selector" != "$fault_selector" ]; then
      echo "broken-selector scenario is not injected; selector is $selector" >&2
      exit 1
    fi
    sleep 5
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      addresses=$(endpoint_addresses)
      if [ -z "$addresses" ] && resource logs deployment/traffic-generator --since=4s --tail=20 | grep -Fq '"status":502'; then
        echo "observed no payments-api endpoints and checkout HTTP 502"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "payments-api endpoints did not disappear with checkout failures within 120 seconds" >&2
    exit 1
    ;;
  reset)
    if [ "$selector" = "$baseline_selector" ]; then
      echo "broken-selector scenario is already reset"
      exit 0
    fi
    if [ "$selector" != "$fault_selector" ]; then
      echo "refusing to replace unexpected payments-api selector: $selector" >&2
      exit 1
    fi
    resource patch "$service" --type=json -p '[{"op":"test","path":"/spec/selector/app","value":"payments-api-disconnected"},{"op":"replace","path":"/spec/selector/app","value":"payments-api"}]'
    sleep 5
    attempt=0
    while [ "$attempt" -lt 60 ]; do
      addresses=$(endpoint_addresses)
      if [ -n "$addresses" ] && resource logs deployment/traffic-generator --since=4s --tail=20 | grep -Fq '"status":200'; then
        echo "restored payments-api endpoints and checkout HTTP 200"
        exit 0
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
    echo "payments-api endpoints or checkout did not recover within 120 seconds" >&2
    exit 1
    ;;
  *) usage ;;
esac
