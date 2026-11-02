# Runtime performance audit — 2026-11-02

## Summary

The audit found avoidable synchronous work that can delay both message delivery and administrative requests. The strongest evidence is **after Replyer has already produced a send tool call**: repeated recovery-chain validation can block the event loop before the adapter is called. A separate long-topic membership scan grows with historical topic IDs even though message bodies are capped at 500.

This is a local synthetic audit, not a production CPU diagnosis. A reported “12% CPU” is not interpretable without the reporting denominator, available cores, container quota, and simultaneous event-loop latency. No external model, QQ account, or production conversation was used.

## Findings and priority

| Priority               | Finding                                                                           | Evidence                                                                                                               | Action/status                                                                                                                                                                       |
| ---------------------- | --------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| High                   | Repeated full recovery-chain scans during source validation                       | A matched 24-source post-Replyer send fixture performed 170 full-chain scans inside segment validation                 | Synchronous source-snapshot batching integrated and independently security-reviewed; all other guards retained                                                                      |
| High                   | Topic membership queries are quadratic in retained bodies × historical IDs        | 50,000 topic IDs and 500 bodies made 20 scene-state reads consume 2.10–2.37 seconds of CPU                             | Per-call membership Sets remove nested scans without dropping IDs or changing authorization                                                                                         |
| High                   | Scene point lookup fetched and parsed all scenes, and validated recovery twice    | 2,000 lookups over 1,000 synthetic scenes consumed 2.43–2.54 seconds of CPU                                            | Parameterized primary-key query, one mandatory gate, fresh result objects                                                                                                           |
| Medium                 | Normal learning enqueue rescans every pending source to repair admission metadata | Matched 100-message burst: 5,150 recall checks before, 100 after; the old repair loop is quadratic in pending arrivals | Integrated repair skip only immediately after successful normal admission; 15 focused/regression tests and independent review pass                                                  |
| Medium                 | Topic IDs are not bounded by the message-body cache                               | A topic with 50,000 IDs retained 50,020 after another 20 messages while bodies stayed capped at 500                    | Follow-on bounded routing-history index retains exact live references; see [retention policy](topic-routing-retention.md). The earlier Set optimization alone did not fix retention |
| Medium                 | Logging performs synchronous filesystem work for each record                      | 2,000 synthetic records blocked the local event loop for roughly 39 ms with disk output                                | Preserve diagnostic durability; any asynchronous writer needs bounded buffering, rotation, flush and failure semantics                                                              |
| Investigate separately | Slow chat history and meme loading                                                | Separate UI/API audit owners identified adapter-history waiting and thumbnail request churn                            | Do not misattribute these to the scene-state endpoint; validate their own request/cancellation paths                                                                                |

## Matched post-Replyer measurement

The fixture uses the actual Runtime with scripted Planner/Replyer and a mock adapter. It retains 24 source messages and seeds a valid synthetic recovery chain only when `send_validation.started` is logged. This isolates the reported boundary without paying the same large-history cost during setup. Both compared variants include the scene primary-key fix; they differ only in the default source-snapshot validation loop.

| Recovery records | Full scans during validation, before → after | Validation span | Process CPU through heartbeat | Actual delay of a 1 ms heartbeat |
| ---------------- | -------------------------------------------: | --------------: | ----------------------------: | -------------------------------: |
| 100              |                                     170 → 29 |      82 → 42 ms |                  504 → 266 ms |                     466 → 228 ms |
| 1,000            |                                     170 → 29 |    644 → 151 ms |              4,027 → 1,087 ms |                   3,948 → 985 ms |

The heartbeat includes subsequent synchronous guards and promise continuations, not only the named validation span. Full-chain scans until that heartbeat fell from 1,197 to 210. `head()` invocation counts do not fall equivalently: most calls inside the new scope reuse a privately validated, transaction-pinned head, while still checking scope invariants and local gate metadata.

**The remaining delay is material.** Dependency recall checks and extension validation callbacks remain unchanged. This result is not a claim that all sending stalls are solved, nor that production CPU will fall by the same ratio. Earlier historical measurements counted 195 scans; use the matched 170-scan baseline above because it includes the scene lookup optimization.

## Recovery safety boundaries

The candidate is deliberately narrower than an eligibility cache:

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

Short heap deltas are GC-sensitive and are not presented as leak measurements. Image caches, description caches, operation receipts and learning queues have explicit bounds. Real-scene state lifetimes still need separate lifecycle analysis. The follow-on [topic retention policy](topic-routing-retention.md) bounds unprotected routing IDs per scene while preserving exact live references. Recall tombstones are safety authority, not disposable cache entries.

## Async and worker-thread assessment

Adding `async` does not make synchronous SQLite, hashing, JSON parsing or array scans nonblocking. The first changes should remove repeated work and constrain concurrency.

- **Sharp already executes native asynchronous work.** Wrapping it in another Worker can add buffer copies without improving the bottleneck. Keep bounded image concurrency and measure larger inputs before changing architecture.
- **SQLite workers need ownership design.** A safe design requires a single database/authority owner, bounded ordered requests, confined transactions, immediate revocation visibility, cancellation before effects versus unknown outcomes after effects, and shutdown/flush handling. Sharing database objects or moving only selected checks to a worker is not a safe drop-in fix.
- **Asynchronous logging needs a contract.** Decide maximum buffered bytes, backpressure or loss policy, serialized rotation, error reporting and shutdown flushing before replacing synchronous writes. A lazy directory-initialization optimization alone does not remove append blocking.
- **Timers are not hard deadlines during synchronous stalls.** Timeout callbacks cannot run while validation blocks the event loop. Conversely, inserting a yield between a freshness check and adapter invocation creates a new race unless the necessary checks are repeated afterward.

## Remaining work and verification limits

1. Run merged repository contract, focused/integration, type and production-build checks on the exact publication candidate. Isolated passes do not replace merged checks.
2. Complete merged verification of the independently reviewed learning repair change. Its matched isolated 100-message burst verifies 100 recall checks versus 5,150 on the original coordinator; 15 focused/regression tests cover recall, cancellation and retry behavior.
3. Verify the follow-on bounded topic-ID retention implementation on the exact merged candidate, including protected references across awaits and cleanup. Do not generalize its policy to durable authority or receipts.
4. Profile remaining dependency/extension guards separately. Do not broaden the reviewed source-read scope without another security review.
5. Verify chat history and meme request changes at their own API/UI boundaries, including cancellation and repeated navigation.
6. If user-environment access becomes available, collect process/container CPU, event-loop delay, queue depths, recovery-chain length, scene/topic sizes and adapter timing together. Do not record raw chats, credentials or media in performance artifacts.

No full production workload, browser-rendering profile, slow-storage test, exhaustive GC study, or large-image saturation test has been performed. The audit identifies and verifies specific causes; it does not declare the whole system performance problem closed.

### Follow-on bounded topic-ID retention

The follow-on implementation uses a timestamped process-local routing index with `topicHistoryMaxMessages` (default 2,048, range 500–20,000) and the existing cache TTL. Its protected references include:

1. IDs of retained message bodies (at most 500 per scene), preferred within the ordinary cap rather than exempt from it.
2. Exact pending, active-wake and model-WAIT event IDs from the new read-only `TurnWaitCoordinator.retainedEventIds()` accessor.
3. Active task source-snapshot IDs and declared dependencies.
4. Active send-plan target/context IDs and any independently held delivery references.

Each topic's existing ID array is filtered against the bounded index and protected references, preserving order and duplicate semantics. Keep the topic identity, attention/version semantics, uncertain-send protection and operation receipts independently. A permanent `protectedSends` flag must not accidentally protect every historical source ID forever. Never prune durable recall authority.

The audit found an important lifecycle gap: Runtime assigns `context.sourceSnapshots` before reading quoted/history/image context, but it adds recent-message IDs to task dependencies only after those awaits. The follow-on implementation makes the task's live source-snapshot IDs available to trimming before the first await. Using task dependencies alone would be unsafe.

Legacy topic admission also routes a reply using stale topic membership even when its body has left the 500-message window. The bounded implementation explicitly defines this as unavailable-history/fallback behavior rather than promising identical indefinite legacy routing. The newer admission path already requires a retained reply target.

Focused verification covers 650 actual ingestions in both admission modes, a suspended history await before dependency population, pending and WAIT inputs, cap shrink and TTL during the await, recall/corruption, uncertain-send flags, and reclamation after cleanup. Existing topic-admission, edit, unknown/partial and no-replay regressions also passed. This follow-on is separate from the earlier membership-Set optimization; merged type/build/integration checks remain required.
