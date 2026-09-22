# Phase 13: reproducible evaluation

IncidentPilot evaluates deterministic infrastructure behavior separately from model quality. This prevents a passing fixture test from being presented as live-LLM accuracy.

## Contract evaluation

`make eval` validates all machine-readable scenario definitions, then runs the trusted investigator/verifier contracts for the six v1 causes. It also runs prompt-injection projection tests, rejects unauthorized tool requests, exercises backend input and data sanitization, checks remediation policy denial, and verifies LLM/tool-call budgets. It prints the RCA, evidence, safety, and budget matrix described by the project evaluation spec.

The result mode is explicitly `deterministic-contract`. A JSON artifact can be produced without storing credentials:

```sh
go run ./cmd/eval -json > /tmp/incidentpilot-eval.json
```

## Infrastructure evaluation

With the local kind environment healthy, `make eval-kind` sequentially injects, checks, and resets all six faults. A trap resets the active fault if the run is interrupted. This mutates only the disposable `incidentpilot-demo` namespace and never invokes an LLM or remediation.

## Live-model claims

Provider/model quality remains non-deterministic. Before quoting an accuracy or pass rate, run each scenario multiple times with the named provider, model, reasoning configuration, and code revision; retain the investigation JSON and bounded evidence IDs for failed trials. Never cherry-pick one successful run or combine the deterministic contract result with a live-model pass rate. Credentials, prompts containing secrets, and raw provider responses must not be placed in evaluation artifacts.
