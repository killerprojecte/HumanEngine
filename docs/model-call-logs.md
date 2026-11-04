# Model call logs

Open **Model call logs** in the sidebar. The authenticated `GET /api/model-calls` endpoint exposes the same metadata. This feature starts collecting after upgrade; it cannot reconstruct old individual calls from daily totals.

## Meaning of each row

A row is one Router provider attempt, including a retry or fallback attempt. Budget rejection or queue cancellation can produce a failed row without an HTTP request being sent; this is not an HTTP access log. `attempt` is the retry number for that provider candidate, starting at 1. Planner, Replyer, vision, learning and embedding calls are included; model-list discovery is not. Capability probes use their actual selected role. Planner includes participation and other operations routed through the Planner role, not only the final planning step.

The timestamp is the start of the attempt, stored as an ISO UTC instant and displayed in the browser's local time zone. Rows use an increasing completion-record ID for stable pagination; concurrent requests may finish in a different order from their start times.

- **Input / output:** normalized provider-reported token counters, not character estimates.
- **Cache read / write:** explicit provider counters. Unknown is `null` / `—`; a reported zero remains zero. Input includes cached input; do not add cache counters to input again.
- **Duration:** elapsed monotonic time for this attempt, including scheduler wait, request/response transport and parsing. Retry backoff between attempts is excluded. A native image transport fallback inside an attempt remains part of that attempt.
- **Output TPS:** output tokens divided by this total duration in seconds, including prefill and waits. It is an end-to-end rate, **not** decoding speed. Unknown output, failed attempts and zero-duration measurements have unknown TPS. Embedding output is normally zero.
- **Transport:** streaming, synchronous, custom adapter, or unknown. Historical rows remain unknown rather than being retroactively classified.
- **First token (TTFT):** milliseconds from sending the fetch request to the first non-empty text, reasoning, or tool-argument event. Unlike total duration, TTFT excludes scheduler queueing and image preparation. The event can be a delta or a non-empty done/completed snapshot. Response headers, keepalives and role-only events do not count. This may precede visible reply text. Synchronous/custom calls and old records remain unknown; the UI never substitutes duration or zero.
- **First content type:** text, reasoning, or tool arguments, with unknown shown when no meaningful content event was measured. Only the category is stored, never its content.
- **Model:** configured model ID, provider ID and requested upstream API model name captured for this attempt; this does not assert the provider actually served an identical underlying model.

A failed status also displays a bounded reason when known: cancelled, first-content timeout, request timeout, stream interrupted, or request failed. It never exposes raw provider errors.

Failed/aborted attempts remain visible with unknown usage if no validated counters returned. Existing aggregate usage and pricing callbacks are unchanged: the new per-attempt log does not add a second billing event. A failed attempt may have consumed tokens upstream even when its counters are unavailable. No raw provider error text is persisted.

## Storage, access and pagination

Metadata is stored locally in SQLite, capped at **30 days** and **100,000 records**. Expired and excess rows are pruned when a new row is written; reads always hide expired records even on an idle instance. Expiry is not a promise that SQLite free pages or old backups have been securely erased. Existing backup access/retention policies still apply.

The endpoint requires the same admin authentication and recovery gate as other management endpoints, sends `Cache-Control: no-store`, and supports role, exact configured model ID and success/failure filters. Default page size is 50, maximum 100. `beforeId` is an exclusive descending keyset cursor; new completions do not shift subsequent pages. Refresh restarts from the newest records. Filter changes reset pagination, and cancelled/stale fetches cannot overwrite a newer page.

Only IDs, role, status, time, duration, transport mode, first-content latency/type and token counters are persisted. Prompt text, model outputs, tool arguments, credentials, raw errors and scene/person identifiers are not stored in this log. These rows do not alter existing explicit diagnostic settings.

## Reference

The presentation and end-to-end TPS definition follow [CPA Manager Plus event rows](https://github.com/seakee/CPA-Manager-Plus/blob/main/apps/web/src/features/monitoring/model/eventRows.ts) and its [real-time events panel](https://github.com/seakee/CPA-Manager-Plus/blob/main/apps/web/src/features/monitoring/components/RealtimeEventsPanel.tsx). HumanEngine does not import proxy authentication or raw request logs, and does not invent upstream TTFT measurements it cannot observe.

## Per-model streaming control

Open **Models**, edit a model, and use **Response streaming → Enable streaming**. Omitted settings appear enabled for native OpenAI Chat and Responses requests. Turning the switch off saves `streaming: false` for that model. Custom adapters show a disabled switch and retain their existing behavior; embedding requests remain synchronous. Canceling changes uses the existing unsaved-change confirmation, while saving uses the existing model configuration API.

For native streaming, provider and shared role/stage deadlines remain active until the first meaningful content event. That first event releases both deadlines, allowing the response to finish; caller/manual cancellation remains effective. Synchronous requests and custom adapters retain their existing full-response deadlines.

There is no extra total or idle timeout after meaningful streamed content starts. A silent open connection can retain a bounded scheduler slot until the provider closes or the caller cancels. Scene invalidation, shutdown and explicit caller cancellation remain authoritative. The SSE reader is capped at 8 MiB, rejects incomplete terminal/tool output, and releases its reader on success or failure. A partial stream is never automatically replayed through retries or a fallback model.

For native streaming only, a scoped dispatcher override disables Node's implicit response-body idle timer. It borrows the currently configured dispatcher (including its proxy policy), does not change global networking, and does not own or destroy shared pools. The provider/role pre-content deadlines still apply. Non-streaming requests and custom adapters do not receive that override. This uses the pinned Undici v7 dispatcher API; see the [official bodyTimeout contract](https://github.com/nodejs/undici/blob/v7.30.1/docs/docs/api/Dispatcher.md).

Compatible gateways that ignore `stream: true` and return complete JSON are accepted once without replay, logged as synchronous, and have unknown TTFT. Gateways that reject streaming can be configured explicitly with `streaming: false`; HumanEngine does not silently resend the same request in another mode.

The log keeps its existing end-to-end TPS definition. Do not use TTFT to infer decoding speed or subtract it from total duration to invent a generation-only rate.

For visual acceptance without upstream requests, run the UI on port 3140 with a fresh `HUMANENGINE_DB`, then run `python3 scripts/streaming-ui-preview.py` and open port 3141. Every management API request is fixture-only; unknown routes fail closed. The preview includes text/reasoning/tool streams, synchronous/custom/historical rows and a failed stream with unknown TTFT. Model edits in this preview stay in process memory.

## Diagnosing slow loading

The call page fetches at most 50 rows, using an ID cursor rather than loading the complete history. An authenticated API response includes `Server-Timing` durations in milliseconds: `recovery` covers the middleware's two full recovery checks, `handler` includes the route and its own store-level recovery check, and `total` covers their combined server work. These timings exclude network transit, browser rendering and engine startup. They contain no credentials, prompts or call metadata. Inspect the request timing alongside this header before attributing latency to the table query.

Recovery checks intentionally read and validate the full authority chain on every check. Head-only checks validate the same rows, hashes, continuity and event schemas but discard the parsed event result array, reducing temporary allocation without caching authorization decisions or hiding changes from other connections. A large authority ledger can still dominate an otherwise small call-log query; these checks must not be disabled to improve page latency.

## Interpreting zero cache counters

Planner includes both participation decisions (`participation.decide`, including silent/wait turns) and reply planning. Their reusable history boundaries are maintained independently. When the bounded context window and a supported model's explicit-cache setting are enabled, both paths mark their current stable history end and preserve a previous successful end while its current authorized prefix still matches. Time, current decisions and other volatile state are rebuilt in the unmarked tail. Withdrawn, changed or inaccessible sources are never retained just for caching.

Markers are request hints, not proof of a cache hit. Auto mode does not assume that a third-party OpenAI-compatible gateway supports explicit markers; only enable the OpenAI option after confirming its capability. OpenAI's minimum applies to the cacheable prefix, not merely total input tokens. Missing usage fields remain unknown; explicit provider zeroes remain zero. A local synthetic transport test proves serialization and accounting, not production cache availability. Diagnose a zero using the configured protocol/cache mode and the provider's returned usage counters, without collecting prompts, outputs or credentials. See the [OpenAI prompt-caching guide](https://developers.openai.com/api/docs/guides/prompt-caching).
