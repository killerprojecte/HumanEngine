# Operation-local memory support read statements

`AssertionLifecycle.refreshSupport()` still performs the complete synchronous
privacy operation inside its existing revision transaction. The only change is
that its repository reuses compiled handles for these two exact reads until that
refresh call returns:

- `SELECT * FROM memories WHERE id=?`
- `SELECT value FROM memory_supports WHERE memory_id=?`

## Repeated work and boundary

A shared positive provenance answer does not avoid parent-row retrieval:
`hasProvenance` retrieves the parent and checks its binding before entering the
recursive function and consulting the positive memo. Shared-parent graphs can
therefore prepare the same parent query once per edge. Root proof reads also
prepare identical SQL repeatedly. If invalidation is needed, current reverse-graph
construction reads proofs again. This change removes repeated SQL compilation,
not any of those reads or graph traversals.

Each call still executes SQL and decodes JSON anew. `get()` still checks recovery
readiness before every lookup. Source recall checks, historical state/revision
reads, provenance budgets, root order, eager record parsing, error propagation,
all descendant purges and revision/vector/workspace effects are unchanged. There
is no row, raw JSON, proof result, source permission or authority-head cache.

Handles are private, prepared lazily at the first existing read, and never
returned to callers. Every nested scope has independent handles and restores its
outer scope in `finally`; the outermost scope releases all references on success
or exception. Nothing survives into a later refresh or an asynchronous callback.
Outside this scope, repository preparation follows the original per-call path.
Query errors retire the scope's handles. SQLite schema revalidation remains in
force; reentrant SQLite callbacks follow the original prepare path, including its
busy-connection failure rather than reusing an executing handle.

## Synthetic evidence

Run `node --import tsx scripts/memory-support-statements-benchmark.ts`.
It uses only a temporary synthetic database: 1,001 records, 1,000 verified children
sharing one manual parent, 512-character content padding, all supported. It invokes
the actual complete `refreshSupport()` operation, alternating ordinary per-call
preparation with the operation-local handle scope. File-backed measurements retain
the normal recovery authority checks. The timing phase does not instrument reads. The harness overrides inherited authority and
log directories with fixture-owned paths, restores the original environment in
`finally`, and protects constructor failures with the same cleanup. Log delivery
is drained before closing/removing the fixture. The fixture-isolation tests use
operator-directory sentinels without running the benchmark workload.

A local Node 24.22.1 run measured median process CPU:

| Fixture     | Per-call preparation | Local handles | Ratio |
| ----------- | -------------------: | ------------: | ----: |
| In-memory   |             60.18 ms |      28.81 ms | 2.09× |
| File-backed |            124.26 ms |      75.92 ms | 1.64× |

A separate count pass observed 1,000 record and 1,000 proof preparations become one
of each. SQL executions, JSON parsing, source checks and authority checks are not
eliminated. This fixture has a short synthetic recovery ledger; it does not predict
all production graphs, large ledgers, or whole-engine CPU savings. The global
scan remains O(records + proof traversal work); this is not pagination or deferred
privacy maintenance.

## Verification

`tests/memory-support-read-statements.test.ts` checks fresh values and parsed
objects after same-operation writes, unchanged recovery-check frequency, scope
release on success/failure, nested rollback restoration, schema/drop/recreate,
closed/new connections, null/duplicate/malformed JSON, reentrant SQLite behavior,
and a real independently advanced recovery authority during a warm scope.
Existing proof lifecycle, history lifecycle, bounded traversal and recovery
statement tests cover the unchanged surrounding privacy behavior.
