# Fixed dependency guard batching

## Boundary and evidence

The follow-on to the [performance audit](performance-audit-20261102.md) profiles actual Runtime execution with scripted Planner/Replyer, 24 synthetic sources, a mock adapter and a temporary durable recovery database. No external provider or live account is called. The fixture seeds a valid chain at `send_validation.started`, after Replyer has produced its send tool call.

The old validation's **29 full chain scans** break down exactly as:

- 1 initial eligibility gate;
- 1 scene participation read;
- 3 fixed source-snapshot validation scans;
- 24 dependency recall scans.

The dependency loop now uses a separate fixed-data read-only snapshot and requires **3** full scans regardless of dependency count. Consequently validation requires **8** full scans. Through the first heartbeat, seven eligibility calls account for 56 scans, while two reply-target recall checks, three settings reads, two final gates and one scene read account for another 8: **64**, down from **211** (7 × 29 + 8). `head()` calls are not full-chain scans: calls within snapshots still enforce connection and transaction invariants.

Illustrative instrumented measurements on the same local host:

| Synthetic chain | Validation scans | Validation wall time | 1 ms heartbeat observed delay |
| --------------- | ---------------: | -------------------: | ----------------------------: |
| 100             |           29 → 8 |       48.4 → 38.0 ms |              282.3 → 203.0 ms |
| 1,000           |           29 → 8 |      170.4 → 67.7 ms |            1,045.5 → 578.2 ms |

Wall times include instrumentation, synchronous diagnostics and host scheduling; they are not production latency guarantees. A later candidate run with additional stack accounting measured 73.3 ms / 665.1 ms for 1,000 records, with exactly the same 8 / 64 counts. Count assertions, rather than timing thresholds, guard the regression.

## Safety contract

- Only primitive scene/message identifiers are accepted by the new dependency entrypoint. The input iterable is copied before snapshots open; no caller iterator executes under reused recovery authority.
- Arbitrary extension callbacks retain their order and run outside the scope. Their recall changes and added dependencies are observed by the following batch. An immediate signal check also rejects a callback that aborts and returns true.
- Runtime-local recalled IDs still reject eligibility. Empty dependency sets introduce no new recovery scans.
- The private implementation accepts a discriminated source/dependency data request, not a caller callback. The existing source snapshot entrypoint remains unchanged.
- Main WAL and authority DELETE-journal transactions are read-only and synchronously pinned. Every reused head checks both transaction states, query-only settings, change counters, schemas and binding. The final read checks these invariants again.
- Scope cleanup occurs in `finally`; the full recovery gate runs again after unlocking. Nested transactions and unsupported configurations retain the original fully checked path.
- No cached validity survives the function, an await or a turn. Final dispatch checks, receipt handling, unknown delivery and no-resend behavior are unchanged.

The focused tests exercise bounded counts, empty/invalid input, iterator mutation before locking, extension recall/abort/rejection/new dependencies, recall across an await, external corruption and append, post-unlock corruption, write denial, contention cleanup, rollback of injected final-read mutation and nested transaction ownership. Existing source-snapshot, send-dispatch, planner, source-event, memory-read-policy and tool-result regressions also run.

## Cached tool-result owner guards

The cache-owner guard also contains two fixed loops: durable dependency recall and captured source snapshots. They now invoke the same fixed-data APIs, preserving dependency-before-source short-circuit ordering and skipping empty snapshot sets. The existing captured/adopted extension guards remain separate, fresh calls outside snapshots. An isolated real cache-owner fixture with 24 unversioned sources measures **120 → 6** full scans; it also proves an extension callback runs again after an await and its recall prevents access. Versioned snapshots continue to use the source-event revision/body-hash validator.

## Extension and worker assessment

The baseline fixture's only extension validation is the built-in source-snapshot validator. General plugin callbacks, model-read-policy tickets, memory/relation/state exposure checks, experience snapshots, image/library guards and habit identity guards have distinct freshness semantics. They are not assumed pure and must not be placed under a generic recovery-head cache. A callback may mutate policy or authority; source and dependency batches deliberately remain separate across such callbacks.

This change removes redundant dependency-chain work but does not eliminate synchronous recovery hashing or SQLite access. Offloading those checks alone would break database ownership and ordered revocation/effect boundaries. A future worker design would require a single database/authority owner, bounded ordered requests, confined transactions, cancellation/unknown-effect rules and shutdown handling; no such architectural migration is included here. Remaining chain-length-dependent latency is an explicit limitation, not evidence that a generic worker wrapper is safe.
