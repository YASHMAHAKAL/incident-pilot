# Phase 6: LLM provider boundary

`internal/llm` defines IncidentPilot-owned chat messages, tool definitions/calls, usage, and the `Provider.Chat` interface. `NewProvider` selects the Groq or Ollama adapter from configuration. Agent logic in Phase 7 depends only on this package's types and interface.

Configuration for the [Phase 7 agent command](phase-7-investigator.md):

| Variable | Groq | Ollama |
| --- | --- | --- |
| `INCIDENTPILOT_LLM_PROVIDER` | `groq` | `ollama` |
| `INCIDENTPILOT_LLM_MODEL` | An installed/supported model ID | A locally available model ID |
| `INCIDENTPILOT_LLM_API_KEY` | Required; supply as a runtime secret | Unset |
| `INCIDENTPILOT_LLM_ENDPOINT` | Unset; fixed official HTTPS endpoint | Optional; defaults to `http://127.0.0.1:11434/api/chat` |
| `INCIDENTPILOT_GROQ_REASONING_EFFORT` | GPT-OSS only: `low` (default), `medium`, or `high` | Unset |

Configuration is inactive until a provider is selected. No checked-in API key or model is used. The current API and kind deployments do not call an LLM; the one-shot Phase 7 agent command instantiates this provider. Models selected for investigation must actually support local tool calling. If a required tool call or JSON response is absent or malformed, the adapter returns an error instead of silently treating it as a successful result.

The boundary sends nonstreaming requests with a 60-second timeout, a 1 MiB request/response cap, at most 64 messages, 16 tools, and 8192 requested output tokens. Groq uses its fixed HTTPS Chat Completions endpoint; Ollama uses `/api/chat`. Redirects are disabled. Provider errors do not include response bodies, which may contain sensitive prompt details. For Groq HTTP 400, only allowlisted error categories are retained; HTTP 429 carries a bounded `Retry-After` duration. OTel spans include provider and token counts, never prompts, tool arguments, or credentials. Returned tool names must match advertised definitions; this is validation, not permission to execute them.

Both adapters translate follow-up tool results. Groq uses a tool call ID and JSON-encoded argument string; Ollama uses a tool name and argument object. Ollama does not provide stable tool call IDs, so the adapter assigns local IDs for a response. JSON output and tool calling use separate turns. The final analysis accepts a provider-neutral JSON schema: Groq maps it to strict Structured Outputs on explicitly supported models, and Ollama sends it as `format`. Groq fails clearly for an unsupported strict-schema model rather than silently downgrading to JSON-object mode. Deterministic application validation remains necessary even with a schema.

Run `make check` for the fixture-backed adapter and configuration tests. A live provider call requires external Groq credentials or a running Ollama model and is not part of the repository checks.

Protocol references: [Groq Chat API](https://console.groq.com/docs/api-reference), [Groq local tool calling](https://console.groq.com/docs/tool-use/local-tool-calling), [Ollama chat API](https://github.com/ollama/ollama/blob/main/docs/api.md), and [Ollama tool calling](https://github.com/ollama/ollama/blob/main/docs/capabilities/tool-calling.mdx).
