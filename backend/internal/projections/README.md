# Deterministic projection owner

This package ports `memory/storage/projection-outbox.ts` and
`memory/projections/{processor,coordinator}.ts`. It stores metadata-only work,
real SQLite FTS5 lexical rows, and person projection state. It does not extract
facts, call models, embed content, send messages, or replay work at startup.

`New` constructs outbox then FTS/person schema even when the feature is off.
The mandatory backend `sqlite_fts5` build profile is checked explicitly; missing
FTS never becomes an ordinary table or a disabled/no-op projection owner.
`Committed` enqueues lexical then person only when enabled. `Flush` drains at
most the source's existing 64-job policy when enabled. `Remove` always deletes
lexical rows, person state, then invalidates pending/leased jobs, regardless of
the feature flag. The enclosing facade owns any best-effort error handling of
Flush, as in the source service; this package returns those failures.

Bind revision callbacks using `CommittedFrom` and `RemoveFrom`; they borrow the
exact active assertion write session. `AssertionReader` is the production
eligibility implementation: repository Get, non-internal use, and Supported.
It deliberately does not require CurrentApplicable, so future-valid records
are indexable without granting current read authority. Each completion receives
the active child capability; suspended ancestors and escaped children retain
the existing recovery capability rejection rules.

Outbox operations preserve source duplicate-before-clock behavior, whole-table
stale invalidation before lease expiry and due ordering, before/after callback
ownership checks, ordinary callback savepoint rollback, three-attempt retry
policy, backoff, and 4096 obsolete terminal retention. Current-snapshot terminal
idempotency and all in-flight work are retained without a new total-table cap.
Native transaction loss permanently revokes the shared root and cannot fall
through to autocommit.

The projection owner and its revision callback adapters are complete local
composition boundaries, not a claim that the overall MemoryService, assertion
recall, runtime startup, working-set, semantic or knowledge domains are wired.
