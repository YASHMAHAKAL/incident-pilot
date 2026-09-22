# Phase 10: policy-governed remediation

Phase 10 introduced one structured `request_remediation` boundary. It accepts a proposal, reconstructs security-relevant facts in trusted Go code, evaluates an embedded Rego policy, and atomically stores the proposal, policy input/decision, and audit event in PostgreSQL. Phase 11 now continues an allowed decision into constrained GitHub patch and PR creation through the same boundary; see the [Phase 11 guide](phase-11-github-remediation.md). Neither phase mutates Kubernetes directly.

```text
MCP request_remediation
  -> fixed authenticated API endpoint
  -> syntactic validation
  -> persisted ROOT_CAUSE_FOUND report + exact cited evidence
  -> trusted target/risk derivation
  -> embedded OPA/Rego decision
  -> one transaction: proposal + decision + audit event
```

The model cannot choose the API URL, policy, configured repository, configured path, environment, or policy version. The API verifies that the investigation belongs to the incident, that its deterministic verifier recorded a supported cause, and that the proposal cites exactly the root cause evidence IDs. It reloads those evidence rows and derives the observed memory limit from the bounded Deployment observation. A caller-supplied `before` value must match that observation.

The v1 allow rule is intentionally narrow: development only, `YASHMAHAKAL/incident-pilot`, `deploy/kind/30-demo.yaml`, the `incidentpilot-demo` `payments-api` Deployment/container, an evidence-verified `memory_limit_oom`, and an `update_resource_limit` proposal that increases the observed MiB limit by no more than 4x and to no more than 512 MiB. The local OOM proposal is `48Mi -> 128Mi`. Repository/path mismatches, protected namespaces, Secrets, direct cluster patches, deletion, unsupported causes or targets, missing evidence, and unsafe resource changes are denied or rejected. Policy evaluation fails closed.

## Local configuration

The kind manifests use a separate disposable remediation token and configure both services:

- API: `INCIDENTPILOT_REMEDIATION_TOKEN`, `INCIDENTPILOT_ENVIRONMENT`, `INCIDENTPILOT_REPOSITORY`, and `INCIDENTPILOT_REMEDIATION_PATH`
- MCP: fixed `INCIDENTPILOT_REMEDIATION_ENDPOINT` plus `INCIDENTPILOT_REMEDIATION_TOKEN`

These checked-in credentials are only for the disposable local cluster. `.env.example` contains matching port-forward values. Do not place provider keys, GitHub tokens, or other credentials in a proposal.

With the API forwarded to port 18080, a proposal has this form:

```json
{
  "incident_id": "<incident UUID>",
  "investigation_id": "<ROOT_CAUSE_FOUND investigation UUID>",
  "operation": "update_resource_limit",
  "target": {
    "repository": "YASHMAHAKAL/incident-pilot",
    "path": "deploy/kind/30-demo.yaml",
    "namespace": "incidentpilot-demo",
    "kind": "Deployment",
    "name": "payments-api",
    "container": "payments-api"
  },
  "change": {"field": "memory_limit", "before": "48Mi", "after": "128Mi"},
  "reason": "The verified OOM was observed with a 48Mi limit.",
  "evidence_ids": ["<pod evidence UUID>", "<deployment evidence UUID>"]
}
```

`POST /api/v1/remediations` returns HTTP 201 for valid proposals; the structured status distinguishes `POLICY_DENIED`, `PR_CREATED`, and `PR_FAILED`. `GET /api/v1/remediations/{proposal-id}` retrieves the durable result. Both endpoints require the remediation token, not the webhook token. Invalid or unverifiable proposals return a generic error and cannot reach policy execution.

Only investigations created by Phase 10-aware binaries contain the trusted machine-readable cause key. Older persisted reports remain readable but fail closed with HTTP 422 if used for remediation; they are not inferred or silently backfilled from prose.

The embedded policy is compiled once at API startup and its prepared query is reused. OTel spans cover remediation receipt, trusted evaluation, policy evaluation, and storage. Metrics count remediation outcomes and policy denials with bounded labels; logs and spans never include proposal bodies or credentials.

## Deliberate boundary

Phase 10 proves that a safe PR-type proposal can be allowed and dangerous proposals are denied. An allow decision alone is not evidence that a repository patch is applicable. Phase 11 therefore re-reads repository content, constrains the exact patch, and creates a reviewable branch/commit/PR through this same decision boundary. It does not turn an allow result into a direct cluster mutation.
