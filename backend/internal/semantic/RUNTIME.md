# Ephemeral semantic learning owner

`NewCoordinator` requires a concrete model-read policy, concrete NativeModels,
a source-resolved ICU collator and an explicit domain Owner. No production
fallback accepts arbitrary snapshots, writes candidates without a transaction,
or fabricates policy validity. `RequestProposals` exposes a narrow completion
port for boundary tests; production construction accepts NativeModels only.

Notifications alone enroll sources. Pending partitions use the source defaults
(30s quiet, 120s maximum wait, 60s minimum interval, 128 sources per partition,
1024 total and 128 partitions). They are source product limits, not added safety
caps. The exact trusted anchor object (or person) identifies a partition. No
restart reads, periodic model job, implicit retry, or persisted runnable queue
exists. A timer only waits for already-enrolled work.

`ConfigurePatch` preserves omitted option fields, including an explicit zero.
`Configure` replaces the complete option value. Both validate atomically and
invalidate pending tickets/revisions before resuming. Decreasing capacities
does not evict existing accepted entries. Scene cancellation does not replay
partially attempted work.

Input capture retains complete original assertions and their ordered JSON.
It uses JS UTF-16 ordering and source Zod code-point string bounds; `inputKey` is the exact
source JSON hash. Proposal request construction uses the existing native
ContextProjection, source system text and source Zod JSON schema. The 65536-byte
limit counts complete canonical messages and tools. There is one managed
learning completion, with priority 2 and a fresh maintenance budget context.
WithModelReadGuard fences every physical native attempt, including accounting
OFF. No direct HTTP or TypeScript proxy is used.

The Owner must provide real authority resolution and existing writer
transactions for attempt receipts and candidate commit. The commit callback
receives the exact active `recovery.WriteSession`; both `Policy.ValidInSession`
and `Owner.ValidInSession` check within that writer, rather than reopening a
root read while the writer is held. The callback must be invoked immediately
before candidate writes. This package does not claim the unresolved candidate
admission/resolution domain or app composition is implemented by its tests.

`WhenIdle` matches the source queue/active-work contract. `CloseAndWait` also
joins scheduled callbacks and actual model/commit/receipt cleanup. Cancellation
alone cannot make active work appear drained. The caller retains ownership of
the shared collator, model host, and database and must close this coordinator
before those dependencies.

Tests use only synthetic records and temporary databases. The source oracle
executes actual capture/request/coordinator/admission TypeScript and records
source hashes. It is test tooling only, never a backend dependency.
