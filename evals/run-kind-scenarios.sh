#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
kubectl_bin=${KUBECTL:-kubectl}
export KUBECTL="$kubectl_bin"

scenarios="oom-memory-limit bad-image invalid-configmap broken-service-selector cpu-latency downstream-failure"
active=""

cleanup() {
  if [ -n "$active" ]; then
    sh "$root/scenarios/$active/run.sh" reset >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

printf '%-30s %s\n' Scenario Infrastructure
for scenario in $scenarios; do
  active=$scenario
  sh "$root/scenarios/$scenario/run.sh" inject
  sh "$root/scenarios/$scenario/run.sh" check
  sh "$root/scenarios/$scenario/run.sh" reset
  active=""
  printf '%-30s %s\n' "$scenario" PASS
done
