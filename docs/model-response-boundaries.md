# Learning model response and timeout boundaries

An empty learning output is not necessarily a failed request. Source extraction deliberately permits no proposal for casual chatter. A completed native response with explicit empty/null text (Chat), or an empty output array (Responses), remains valid. Reasoning-only completed output also remains valid; private reasoning is neither exposed nor interpreted as a memory candidate.

Model IO diagnostics now include a bounded `responseSummary` (protocol, terminal reason, reasoning presence) and normalized usage. Flow entries distinguish `empty_model_output` from `reasoning_only_response`. Missing Chat content without a tool-call array, a `tool_calls` finish without calls, a refusal, or an unsupported Responses content type is not silently treated as successful silence. Refusals are reported using `model_response_refused`, without copying refusal content into failure logs.

## Two independent configured deadlines

- Models → provider → request timeout is the **per-attempt** timeout (`providers[].timeoutMs`, default 30,000 ms).
- Models → role route → timeout is the **whole operation** budget (`routes[].timeoutMs`), including scheduler wait, provider attempts/backoff and ordered model fallback. Increasing only the provider timeout does not override a shorter role budget.
- `model_request.started` records `stageBudgetMs`; provider/model attempt entries record `timeoutMs`. Terminal failures distinguish `model_stage_timeout`, `provider_timeout` and caller `request_cancelled`, with the existing trace/batch identifiers and sanitized error context.
- A provider timeout has an uncertain remote outcome and is not automatically retried on that provider, even if retry count is nonzero. Explicit retryable HTTP failures retain their bounded retry policy. Configured ordered model fallback remains a separate existing policy; this change does not remove it.
- Learning's 15,000 ms minimum interval is an admission cadence, not an active request timer. Enqueuing new messages or foreground work does not preempt an active learning batch. Source recall, disabling learning and runtime disposal still cancel work intentionally.

A local HTTP regression completes learning after 15 seconds when both configured deadlines permit it. This establishes the source-level behavior, not the cause of a particular production disconnection. A gateway, custom adapter, deployment version or retained route/provider setting can impose a separate limit. No provider credentials or paid requests are required by these regressions.
