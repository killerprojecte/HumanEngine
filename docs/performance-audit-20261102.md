# Runtime performance audit — 2026-11-02

## Summary

The audit found avoidable synchronous work that can delay both message delivery and administrative requests. The strongest evidence is **after Replyer has already produced a send tool call**: repeated recovery-chain validation can block the event loop before the adapter is called. A separate long-topic membership scan grows with historical topic IDs even though message bodies are capped at 500.

This is a local synthetic audit, not a production CPU diagnosis. A reported “12% CPU” is not interpretable without the reporting denominator, available cores, container quota, and simultaneous event-loop latency. No external model, QQ account, or production conversation was used.

## Receive-only follow-up

The subsequent [bounded load validation](performance-load-validation-20261102.md) uses the actual Engine with replies, learning and proactive behavior disabled, and asserts zero model calls. It reproduces administrative latency from ordinary incoming text: feedback admission unnecessarily projected the full cached history before checking whether a reply target existed. Repeated identity writes and recovery-chain proof work compounded the cost. The follow-up fixes add early no-work exits, exact identity write suppression, SQL-prefiltered meme references, conservative idle-scene cleanup, fixed dependency read scopes, and bounded exact-row SHA proofs. No recovery head, permission decision or parsed event is cached.

The matched synthetic workload improved from 111.14 to 26.16 seconds of process CPU for 2,000 arrivals; settings HTTP p95 fell from 779.05 to approximately 153 ms. These are combined local workload measurements, not a production guarantee. Fresh SQL reads, event parsing and other synchronous work remain. The small no-applicable-media early return was added after those measurements and has separate regression coverage, not an attributed benchmark speedup.

## Findings and priority

| Priority               | Finding                                                                           | Evidence                                                                                                               | Action/status                                                                                                                                                                       |
| ---------------------- | --------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| High                   | Repeated full recovery-chain scans during source validation                       | A matched 24-source post-Replyer send fixture performed 170 full-chain scans inside segment validation                 | Synchronous source-snapshot batching integrated and independently security-reviewed; all other guards retained                                                                      |
| High                   | Topic membership queries are quadratic in retained bodies × historical IDs        | 50,000 topic IDs and 500 bodies made 20 scene-state reads consume 2.10–2.37 seconds of CPU                             | Per-call membership Sets remove nested scans without dropping IDs or changing authorization                                                                                         |
| High                   | Scene point lookup fetched and parsed all scenes, and validated recovery twice    | 2,000 lookups over 1,000 synthetic scenes consumed 2.43–2.54 seconds of CPU                                            | Parameterized primary-key query, one mandatory gate, fresh result objects                                                                                                           |
| Medium                 | Normal learning enqueue rescans every pending source to repair admission metadata | Matched 100-message burst: 5,150 recall checks before, 100 after; the old repair loop is quadratic in pending arrivals | Integrated repair skip only immediately after successful normal admission; 15 focused/regression tests and independent review pass                                                  |
| Medium                 | Topic IDs are not bounded by the message-body cache                               | A topic with 50,000 IDs retained 50,020 after another 20 messages while bodies stayed capped at 500                    | Follow-on bounded routing-history index retains exact live references; see [retention policy](topic-routing-retention.md). The earlier Set optimization alone did not fix retention |
| Medium                 | Logging performs synchronous filesystem work for each record                      | 2,000 synthetic records blocked the local event loop for roughly 39 ms with disk output                                | Bounded asynchronous workers now provide explicit loss/flush health; see the matched logging workload and tradeoffs                                                                 |
| Investigate separately | Slow chat history and meme loading                                                | Separate UI/API audit owners identified adapter-history waiting and thumbnail request churn                            | Do not misattribute these to the scene-state endpoint; validate their own request/cancellation paths                                                                                |

## Matched post-Replyer measurement

The fixture uses the actual Runtime with scripted Planner/Replyer and a mock adapter. It retains 24 source messages and seeds a valid synthetic recovery chain only when `send_validation.started` is logged. This isolates the reported boundary without paying the same large-history cost during setup. Both compared variants include the scene primary-key fix; they differ only in the default source-snapshot validation loop.

| Recovery records | Full scans during validation, before → after | Validation span | Process CPU through heartbeat | Actual delay of a 1 ms heartbeat |
| ---------------- | -------------------------------------------: | --------------: | ----------------------------: | -------------------------------: |
| 100              |                                     170 → 29 |      82 → 42 ms |                  504 → 266 ms |                     466 → 228 ms |
| 1,000            |                                     170 → 29 |    644 → 151 ms |              4,027 → 1,087 ms |                   3,948 → 985 ms |

The heartbeat includes subsequent synchronous guards and promise continuations, not only the named validation span. Full-chain scans until that heartbeat fell from 1,197 to 210. `head()` invocation counts do not fall equivalently: most calls inside the new scope reuse a privately validated, transaction-pinned head, while still checking scope invariants and local gate metadata.

**The remaining delay is material.** Dependency recall checks and extension validation callbacks were unchanged in this particular comparison. Later fixed-list dependency batching and pure row-hash proof reuse are documented below; these historical measurements must not be presented as measurements of the later combined implementation. This result is not a claim that all sending stalls are solved, nor that production CPU will fall by the same ratio. Earlier historical measurements counted 195 scans; use the matched 170-scan baseline above because it includes the scene lookup optimization.

## Recovery safety boundaries

The published source-validation optimization is deliberately narrower than an eligibility cache:

- It accepts only the fixed source-snapshot list, not an arbitrary callback.
- The main WAL database and DELETE-journal authority database hold synchronous read snapshots with `query_only` enabled.
- The full chain is checked before reuse. Every cached-head access verifies transaction/read-only state, connection binding, change counters and schema versions.
- Scope invariants are checked again after the final source read, before releasing either snapshot.
- The private scope is cleared and write settings restored in `finally`; the full gate is checked again after unlock.
- An existing transaction or unsupported journal/read-only configuration takes the original fully checked path.
- No validity survives an `await`, a turn, or the call itself. No TTL, global permission cache, skipped authority gate, or generic plugin callback API is introduced.
- Final delivery checks remain in place. Same-scope durable append and authority close are rejected.

Focused tests cover external writer blocking, external corruption and valid append detection, post-unlock mutation, busy-timeout cleanup, same-connection write refusal, rollback after an injected final-read mutation, nested transaction ownership, source body-hash mismatch, enrollment boundaries, and recall.

## Other measured workloads

- **Actual unscheduled Runtime ingress:** 9 recovery-head checks, 4 config/settings reads, 2 scene reads and 4 log records per accepted message after the scene lookup fix. Twenty messages with 1,000 synthetic recovery records consumed 580 ms CPU; the slowest individual ingestion was 38 ms. Five-message batches produced a 157 ms observed event-loop delay. This fixture excludes Engine learning/media hooks.
- **Idle Runtime:** a one-second observation registered no gate reads or logs. Harness/GC CPU is not evidence of an application busy loop.
- **Long topics:** 20 scene-state reads with 50,000 IDs and 500 bodies fell from 2,103–2,375 ms CPU to 44–62 ms using membership Sets. With topic admission enabled, 20 ingestions fell from 1,233 ms to 185 ms. Sets are rebuilt per call; they do not cache permission or solve unbounded history retention.
- **Model-work scheduler:** 256 mock asynchronous jobs consumed 18 ms CPU, peaked at 4 running jobs, drained completely, and observed about 3.5 ms maximum event-loop delay. This does not model a real provider or sustained overload.
- **Image byte cache:** 16 concurrent 1 MiB reads coalesced into one load and 15 waiters. Another 32 cache hits consumed about 12 ms CPU but allocated 32 MiB of defensive buffer copies. Do not remove those copies without proving callers cannot mutate shared cache bytes.
- **Native image processing:** two parallel 512-pixel compressions used about 82 ms aggregate CPU over 51 ms wall time with a 2.2 ms maximum event-loop delay. Native asynchronous offload is already effective for this small case; 40-megapixel worst cases were not measured.
- **Logging:** 2,000 records consumed about 35 ms CPU in memory versus 53 ms with a temporary local disk log. Standard output was redirected to a counting sink, so production stdout backpressure and slow volumes remain unmeasured.

Short heap deltas are GC-sensitive and are not presented as leak measurements. Image caches, description caches, operation receipts and learning queues have explicit bounds. At this initial measurement stage, real-scene state lifetimes still needed separate lifecycle analysis; the later conservative cleanup is recorded below. The follow-on [topic retention policy](topic-routing-retention.md) bounds unprotected routing IDs per scene while preserving exact live references. Recall tombstones are safety authority, not disposable cache entries.

## Async and worker-thread assessment

Adding `async` does not make synchronous SQLite, hashing, JSON parsing or array scans nonblocking. The first changes should remove repeated work and constrain concurrency.

- **Sharp already executes native asynchronous work.** Wrapping it in another Worker can add buffer copies without improving the bottleneck. Keep bounded image concurrency and measure larger inputs before changing architecture.
- **SQLite workers need ownership design.** A safe design requires a single database/authority owner, bounded ordered requests, confined transactions, immediate revocation visibility, cancellation before effects versus unknown outcomes after effects, and shutdown/flush handling. Sharing database objects or moving only selected checks to a worker is not a safe drop-in fix.
- **Asynchronous logging requires an explicit contract.** The implemented [delivery policy](asynchronous-log-delivery.md) defines buffered bytes, visible overload loss, serialized rotation, errors and shutdown flushing. A lazy directory-initialization optimization alone would not remove append blocking.
- **Timers are not hard deadlines during synchronous stalls.** Timeout callbacks cannot run while validation blocks the event loop. Conversely, inserting a yield between a freshness check and adapter invocation creates a new race unless the necessary checks are repeated afterward.

## Published verification and remaining work

The first four implementation slices are published through `f1e0006`: learning
admission repair, bounded routing history, chat history reconciliation/cancellation,
and thumbnail queue fixes. That merged candidate passed 1,809 tests, contract/types,
production build and both CI workflows. These are completed implementation checks,
not pending proposals.

Later published changes are also distinct from the earlier measurements:

- `70c4d55` added fixed-list dependency validation, exact meme ID/hash SQL filtering,
  unchanged identity-write avoidance, no-anchor preview bypass, pure exact-row SHA
  proof reuse, and conservative idle scene-state cleanup. Its 1,857 tests and final
  checks/CI passed. Replay-bearing, uncertain-send and active states remain pinned;
  this is not a hard cap on all scenes.
- `634c0b9` added indexed budget queries, existence-only meme iteration and an
  operation-local purge graph. The original local run had 1,865 passes and two stale
  fixture-hash failures. Corrected acceptance, types/build and final full CI passed;
  the original failed command is not relabeled a pass.
- `b6a08ae` narrowed memory exposure and embedding revalidation to exact IDs while
  preserving global lifecycle refresh. The original run had 1,871 passes and two
  obsolete method-counter failures; corrected affected/acceptance groups passed
  47 tests, and final full CI passed. Content/privacy assertions were unchanged.
- `e6e5b2e` moved authorized outbound original-media encoding off the event loop,
  added a nonunique scalar source-event lookup index, and replaced repeated first-ID
  searches with a local Map. All 1,899 tests, contract/types/build and production
  worker byte/startup smoke checks passed before publication. Check CI passed, but
  the container test stage lacked `openssl` for its ephemeral HTTPS certificate
  fixture. Build-stage-only correction `2d4c59f` preserves the TLS test and runtime
  image; its automatic CI must pass before the new image is considered ready.
- The current logging implementation uses [bounded asynchronous delivery](asynchronous-log-delivery.md):
  separate stdout/file workers, explicit saturation/failure health and graceful
  flush. Its [matched workload evidence](logging-performance-validation.md) reports
  mixed CPU effects, additional worker memory, and overload losses explicitly.

[Receive-only measurements](performance-load-validation-20261102.md) exercise replies
and learning disabled with concurrent administration requests. Process CPU fell
from 111.1 to 26.2 seconds in that controlled synthetic workload. Do not attribute
that earlier reduction to logging or promise the same ratio in production.

Remaining work and limitations:

1. Global memory lifecycle invalidation and arbitrary extension callbacks still
   require fresh authority. Narrower scans must not silently change synchronous
   privacy erasure, ranking or failure atomicity. No blanket permission cache or
   automatic database-worker migration is approved by these optimizations.
2. Replay-bearing scene states are different from reclaimable idle cache-only
   states. Durable source authority, receipts and active work are not disposable.
3. Browser-rendering, slow-storage, sustained-memory and large-image saturation
   coverage is not exhaustive. Bounded image/cache and sink-pressure fixtures are
   documented separately; API/queue tests do not establish browser latency.
4. Further production validation needs synchronized process/container CPU,
   event-loop delay, queue depths, chain length and adapter timing. Raw chats,
   credentials, media and private profiles must not enter performance artifacts.

This audit verifies specific causes and fixes; it does not declare every possible
whole-system performance problem closed.

### Follow-on bounded topic-ID retention

The follow-on implementation uses a timestamped process-local routing index with `topicHistoryMaxMessages` (default 2,048, range 500–20,000) and the existing cache TTL. Its protected references include:

1. IDs of retained message bodies (at most 500 per scene), preferred within the ordinary cap rather than exempt from it.
2. Exact pending, active-wake and model-WAIT event IDs from the new read-only `TurnWaitCoordinator.retainedEventIds()` accessor.
3. Active task source-snapshot IDs and declared dependencies.
4. Active send-plan target/context IDs and any independently held delivery references.

Each topic's existing ID array is filtered against the bounded index and protected references, preserving order and duplicate semantics. Keep the topic identity, attention/version semantics, uncertain-send protection and operation receipts independently. A permanent `protectedSends` flag must not accidentally protect every historical source ID forever. Never prune durable recall authority.

The audit found an important lifecycle gap: Runtime assigns `context.sourceSnapshots` before reading quoted/history/image context, but it adds recent-message IDs to task dependencies only after those awaits. The follow-on implementation makes the task's live source-snapshot IDs available to trimming before the first await. Using task dependencies alone would be unsafe.

Legacy topic admission also routes a reply using stale topic membership even when its body has left the 500-message window. The bounded implementation explicitly defines this as unavailable-history/fallback behavior rather than promising identical indefinite legacy routing. The newer admission path already requires a retained reply target.

Focused verification covers 650 actual ingestions in both admission modes, a suspended history await before dependency population, pending and WAIT inputs, cap shrink and TTL during the await, recall/corruption, uncertain-send flags, and reclamation after cleanup. Existing topic-admission, edit, unknown/partial and no-replay regressions also passed. This follow-on is separate from the earlier membership-Set optimization; the final merged type/build/integration checks and CI passed as recorded above.
