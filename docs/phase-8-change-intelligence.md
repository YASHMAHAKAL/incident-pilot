# Phase 8: change intelligence

Phase 8 adds an optional, read-only evidence chain:

```text
incident window
-> fixed Argo CD application state
-> deployments near that window
-> newest correlated revision
-> fixed GitHub repository commit and bounded diff
-> persisted evidence IDs
```

The MCP server exposes `argocd_get_application`, `argocd_get_revision_history`, `github_get_commit`, and `github_get_diff`. Application, project, repository, and upstream URLs are administrator configuration; callers cannot supply them. Argo history is limited to the incident window plus one preceding hour and at most 20 revisions. GitHub reads accept only hexadecimal commit IDs, re-check that the requested revision is present in that bounded Argo history, return at most 30 changed files, and cap each patch at 8 KiB. All upstream calls are GET-only, have deadlines and response-size limits, and are instrumented like the existing MCP tools.

The v1 adapter intentionally supports one single-source Argo CD Application mapped to one configured GitHub repository. Multi-source Applications are rejected rather than guessing which repository owns a revision.

Argo and GitHub responses are projected rather than stored verbatim. Tokens, repository credentials, author email addresses, annotations, and unrelated API fields are excluded. Common credential-shaped values in commit messages and patches are redacted. Commit messages and diffs are still untrusted input; the investigator receives bounded evidence cards and is told that change timing is correlation, not proof. Change evidence can support a hypothesis, but Phase 7's deterministic OOM verifier still decides whether the final OOM RCA is accepted.

Change collection occurs only during the initial incident collection. If change sources are not configured or unavailable, one safe partial failure is recorded and Kubernetes/telemetry investigation continues. Targeted downstream collection does not repeat the external reads.

## Configuration

Set these on the `incidentpilot-mcp` process:

```text
INCIDENTPILOT_ARGOCD_URL
INCIDENTPILOT_ARGOCD_TOKEN
INCIDENTPILOT_ARGOCD_APPLICATION
INCIDENTPILOT_ARGOCD_PROJECT
INCIDENTPILOT_GITHUB_API_URL          # defaults to https://api.github.com when a repository is set
INCIDENTPILOT_GITHUB_REPOSITORY       # owner/repository
INCIDENTPILOT_GITHUB_TOKEN            # optional for public repositories
```

Use an Argo CD account restricted to `get` on the configured application. For a private GitHub repository, prefer a GitHub App installation token or fine-grained token with only repository Contents read permission. The model and agent process never receive either credential.

The kind MCP Deployment optionally imports a user-created `incidentpilot-change-sources` Secret. The repository deliberately does not contain real external credentials. After creating that Secret, restart `deployment/incidentpilot-mcp`. Argo CD itself and a Git remote are not installed or invented by this phase; configure them when the project has a real repository and GitOps application.

The adapters follow the official [Argo CD API authentication/application API](https://argo-cd.readthedocs.io/en/stable/developer-guide/api-docs/) and GitHub [Get a commit](https://docs.github.com/en/rest/commits/commits#get-a-commit) contracts.

## Verification boundary

Automated adapter tests use controlled HTTP transports to verify request methods, fixed routing, authorization headers, window/revision enforcement, result bounds, and credential redaction. Collector tests verify the trusted Argo-to-GitHub chain and persistence behavior. A live Argo/GitHub integration run remains environment-dependent because this local checkout currently has neither an Argo CD application nor a Git remote.
