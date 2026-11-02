# Chat history loading

The chat page requests recent history, not an entire persisted transcript. The SnowLuma
adapter asks for 30 messages; its configured transport timeout defaults to 15 seconds.
Previously the first transcript stayed empty until that external call finished, even
when recent live messages were already retained in process memory. This is a verified
blocking path, not a measurement of any production QQ server's latency.

The page now starts two independent, authenticated requests on scene selection:

- `GET /api/scenes/{id}/history?source=local` returns at most 30 currently retained
  messages, without an external history request. Coverage is explicitly partial.
- The default remote request still asks the adapter for 30 messages. After that wait,
  the server re-reads the current local window, reconciles current recalls and local
  edits, and returns at most 60 deduplicated messages in chronological order. The
  browser replaces its initial snapshot rather than merging stale client text.
  Default remote results omit recalled rows, including on platform failure or adapter
  absence. Explicit local snapshots may show body-empty recalled placeholders until
  the reconciled remote snapshot replaces them.

A platform failure retains only a freshly read local snapshot when the server can
respond. An HTTP/network failure leaves the already visible snapshot explicitly marked
as unrefreshed. Empty local history is not presented as a complete or successful remote
read. Switching scenes/modes and unmounting cancel the request and reject late results;
Node response disconnects are explicitly bridged into the per-request abort signal
because H3's Node `toWebRequest` does not supply that bridge itself. Cancelling one
request does not cancel another consumer or background runtime work.

No transcript persistence, replay, real-platform sends, polling, or external model
requests are introduced. Existing local retention/cache-disable policies still apply.
The current read-only UI still uses manual refresh and does not expose older-history
pagination. Its real-message DOM is bounded at 60 entries, so a virtualization dependency
is unnecessary for this change. Images and identity presentation retain their existing
security and display behavior.

Synthetic tests exercise a stalled adapter with independently available local history,
real local HTTP socket cancellation, request isolation, auth/scope/recall boundaries,
pending live edits/arrivals/recalls, and the actual compiled chat-page loading and scene
switch logic. These tests are not a live-platform or browser visual performance audit.
