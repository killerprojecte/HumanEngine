# Working-set metadata owner

This package ports the optional `src/memory/working-set` live metadata domain:
strict lossless schemas, source field-order serialization, storage, classification,
selection receipts, pin policy, projection generations/leases, same-transaction
mutation hooks, and notification-driven maintenance. It never authorizes content
reads, changes evidence/truth, invokes a model, upgrades old references, or replays
sources at startup. The real MemoryWorkspace/WorkspaceAuthority and model-context
selection/capture owner are **not** implemented by this package. The separate quarantined offline owner is implemented in `offline.go`;
`Storage.Reset` alone is just the live storage primitive, not that owner.
The offline domain does not perform complete reconcile, ACK or ready transition.

## Composition

`New(ctx, main, authority, Ports)` audits the optional four-table layout without
creating it. Schema audit precedes policy validation. `Ports.ResolveCurrent`
receives `*Scope`, the exact authority-verified read snapshot or active write
child, and must resolve only the exact authorized batch. It is an integration
boundary, not a production mock authorization implementation.

- `View` and `ListPinPolicies` use a real read publication snapshot. They do not
  reserve the main SQLite writer. Bound reads borrow the exact active child;
  they do not introduce another savepoint. Publication still uses the existing
  recovery authority verification/lock protocol; this is not a throughput claim.
- `Transaction` and `WithSession` compose writes with the real active
  `recovery.WriteSession`. Each transactional service operation borrows its
  active child. Observer and eligibility callbacks must use that supplied scope,
  never reopen the authority or acquire another main connection.
- A publication `Scope.WriteSession()` is nil. `Scope.Query` is native read-only
  there, including against `INSERT ... RETURNING`, DDL, transaction control,
  attached/temp schemas, and caller PRAGMAs. The private fixed audit temporarily
  admits only its own code and uses `PRAGMA main.table_info`.
- Scope/row lifetimes end at the callback boundary. Native transaction loss
  permanently latches failure instead of allowing accidental autocommit.
- Storage writes require the actual write capability. Unbound storage access
  reports a capability error, never a nil-pointer panic.
- All pin writes require reader capability 4, fixed schema enrollment and a
  durable typed `working_set_pin` intent before the local row. The pin observer
  cannot replace these steps. Outer/nested rollback can remove the local ACK
  while leaving the durable intent; subsequent readiness must fail closed.

`Mutations` is only for the authoritative lifecycle owner in an existing write
scope. The source's batch size 40 and paginated all-scene terminal cleanup are
preserved; no protective admission caps or periodic scans are added. Ordinary
source invalidation preserves independent pin intent, including while disabled.

`Now` and `Token` are injectable. Production uses milliseconds and random UUIDs.
Only source policy limits are enforced. Receipt identity omits dependencyKey;
dedup precedes expiry pruning; expiry pruning is strict `<`. Complete checks the
full lease before resolving and only generation/token afterward, intentionally
not adding another expiry check.

Policy configuration publishes immutable values behind a short lock. Reads take
snapshots at source policy-use sites, including after the resolver callback.
Locks never span eligibility or observer callbacks, so reconfiguration can be
reentrant. Callbacks themselves belong to the composing owner's concurrency
model and must be safe when that owner invokes independent operations in parallel.

## Tests and source oracle

`source_oracle_test.go` consumes the actual TypeScript generator at
`backend/tests/generate-working-set-fixtures.mjs`. It compares accepted schemas,
source-rebuilt property order, named operation errors, results, callback order,
pin observations and the exact JSON text stored in all four tables. The source
oracle's pin hook is explicitly synthetic; real durable intent/ACK gaps are
separately covered by native integration tests, never bypassed for fixture parity.

Native tests cover derived privacy vs pins, receipt dedup/strict expiry, callback
lease changes, 85-scene terminal cleanup, held capabilities, nested rollback,
durable intent/ACK gaps, native transaction loss, read-only publication denials,
main-WAL writer contention, and policy concurrency/reentrancy. Fake scheduler
tests cover a single notification timer, disabled paths, failed reporting and
disposal without timer resurrection.

Required native build profile is `sqlite_fts5`; run package and recovery race
checks plus the source generator check when the shared build window is available.
At the initial implementation handoff, only gofmt syntax/formatting was run;
compilation/test status must be reported separately by the validation owner.
