# Model call logs

Open **Model call logs** in the sidebar. The authenticated `GET /api/model-calls` endpoint exposes the same metadata. This feature starts collecting after upgrade; it cannot reconstruct old individual calls from daily totals.

## Meaning of each row

A row is one Router provider attempt, including a retry or fallback attempt. Budget rejection or queue cancellation can produce a failed row without an HTTP request being sent; this is not an HTTP access log. `attempt` is the retry number for that provider candidate, starting at 1. Planner, Replyer, vision, learning and embedding calls are included; model-list discovery is not. Capability probes use their actual selected role. Planner includes participation and other operations routed through the Planner role, not only the final planning step.

The timestamp is the start of the attempt, stored as an ISO UTC instant and displayed in the browser's local time zone. Rows use an increasing completion-record ID for stable pagination; concurrent requests may finish in a different order from their start times.

- **Input / output:** normalized provider-reported token counters, not character estimates.
- **Cache read / write:** explicit provider counters. Unknown is `null` / `—`; a reported zero remains zero. Input includes cached input; do not add cache counters to input again.
- **Duration:** elapsed monotonic time for this attempt, including scheduler wait, request/response transport and parsing. Retry backoff between attempts is excluded. A native image transport fallback inside an attempt remains part of that attempt.
- **Output TPS:** output tokens divided by this total duration in seconds, including prefill and waits. It is an end-to-end rate, **not** decoding speed. Unknown output, failed attempts and zero-duration measurements have unknown TPS. Embedding output is normally zero.
- **First token:** unavailable for the current complete-response adapter contract and native `stream: false` requests. It is deliberately `null`, never copied from total duration or first response bytes. Adding real streaming measurements later requires measuring the first meaningful content delta and preserving tool/response semantics.
- **Model:** configured model ID, provider ID and requested upstream API model name captured for this attempt; this does not assert the provider actually served an identical underlying model.

Failed/aborted attempts remain visible with unknown usage if no validated counters returned. Existing aggregate usage and pricing callbacks are unchanged: the new per-attempt log does not add a second billing event. A failed attempt may have consumed tokens upstream even when its counters are unavailable. No raw provider error text is persisted.

## Storage, access and pagination

Metadata is stored locally in SQLite, capped at **30 days** and **100,000 records**. Expired and excess rows are pruned when a new row is written; reads always hide expired records even on an idle instance. Expiry is not a promise that SQLite free pages or old backups have been securely erased. Existing backup access/retention policies still apply.

The endpoint requires the same admin authentication and recovery gate as other management endpoints, sends `Cache-Control: no-store`, and supports role, exact configured model ID and success/failure filters. Default page size is 50, maximum 100. `beforeId` is an exclusive descending keyset cursor; new completions do not shift subsequent pages. Refresh restarts from the newest records. Filter changes reset pagination, and cancelled/stale fetches cannot overwrite a newer page.

Only IDs, role, status, time, duration and token counters are persisted. Prompt text, model outputs, tool arguments, credentials, raw errors and scene/person identifiers are not stored in this log. These rows do not alter existing explicit diagnostic settings.

## Reference

The presentation and end-to-end TPS definition follow [CPA Manager Plus event rows](https://github.com/seakee/CPA-Manager-Plus/blob/main/apps/web/src/features/monitoring/model/eventRows.ts) and its [real-time events panel](https://github.com/seakee/CPA-Manager-Plus/blob/main/apps/web/src/features/monitoring/components/RealtimeEventsPanel.tsx). HumanEngine does not import proxy authentication or raw request logs, and does not invent upstream TTFT measurements it cannot observe.
