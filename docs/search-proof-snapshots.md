# Fixed memory-search proof snapshots

> Historical measurements below describe the previous per-operation full-scan implementation. The current [recovery validation model](recovery-row-hash-proofs.md) reuses committed snapshot integrity, validates changed SQLite snapshots, and supports fixed reads inside existing main transactions. Historical scan counts are not the current contract.

The search read now uses one fixed synchronous dual-database read operation. It does not expose a generic callback scope, reusable recovery head, or cached permission result.

## Read and write boundary

`MemoryService.search` still finishes `refreshSupport` before any read-only scope. Its invalidation/revision/projection writes, vector validation, and initial permitted-scope check retain their order. The fixed read receives copied identifiers, terms and vector bytes only; even shared vector backing is copied before snapshots begin. It constructs its own built-in authority and persistence readers. `AssertionHistoryReader` shares the existing state/revision decoding implementation but never initializes or migrates tables.

The candidate scan, current-applicability proof graph, ranking query, final support filtering and sanitization run inside the existing main-WAL/authority-DELETE snapshots. Both connections are query-only. Full recovery-chain verification occurs when locking and again after unlocking; every reused head checks transaction ownership, query-only flags, local change counters, schemas and the main-to-authority binding. The final read checks those invariants too. Errors unwind transactions and restore writable state. Existing transactions, in-memory stores and unsupported journal configurations retain per-read checks.

No callback, await, model work or external request is introduced. No recovery head, proof validity, record cache or authorization result survives a search call. The existing per-call applicability map is unchanged. Normal caller-owned result values are not authority tokens and must still be revalidated at later exposure/use boundaries.

## Preserved search semantics

- Scope/person/simulation isolation, quarantine/internal-use exclusion and tombstones.
- Current source revision and identity checks, historical parent version/deletion-epoch binding, AND parents within each proof and OR proof alternatives.
- Original lexical/vector scoring, confidence/importance weights, stable ranking tie-breakers and limits.
- Per-candidate `Date.now()` applicability evaluation and final SQLite `now` filtering. Candidate selection is still scope-only: it does not prefilter temporal eligibility before traversing the graph.
- Fresh final support filtering and source sanitization before returning results.

## Reproduction and measurements

Run `node --import tsx scripts/memory-search-benchmark.ts <checkout> 1000 24 3`. The optional checkout argument permits the same offline fixture to run against a baseline and candidate without modifying either checkout. All database, authority and log files are temporary and isolated from operator configuration. No model, account or external service is called.

A matched local run on Node 24.22.1 / AMD EPYC 9V74 used 25 records (24 learned records sharing one manual parent), 1,000 additional valid synthetic authority rows, one warm-up and three measured searches. It measures complete `memory.search`, including the unchanged support refresh:

| Measurement                             | Baseline `f3f669a` source tree | Fixed search |
| --------------------------------------- | -----------------------------: | -----------: |
| Full chain scans per search             |                            511 |           53 |
| Median wall time                        |                       905.4 ms |     110.3 ms |
| Median process CPU                      |                     1,009.1 ms |     121.0 ms |
| Median zero-delay heartbeat observation |                       914.1 ms |     112.2 ms |

These synthetic measurements are not production CPU percentages or latency guarantees. The search proof/ranking read itself uses two full scans regardless of graph-read count; the remaining complete-search scans include the deliberately unchanged support refresh and initial scope check. Support-refresh cost still grows with its workload, and full chain verification remains linear in chain length.

Regression tests use scan-count and result assertions rather than timing thresholds. They cover fixed-snapshot write exclusion, external and post-unlock tampering, query-only invariant changes, exception cleanup, non-WAL/nested fallback, source edits, recall/update freshness, input accessors, shared vector bytes, lexical/vector ranking, limits and simulation/person/time isolation. Existing graph, temporal, parent-binding, history and recovery suites remain applicable.
