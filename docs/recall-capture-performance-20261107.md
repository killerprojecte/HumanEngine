# Recall capture read-path repair

> Historical measurements below describe the previous per-operation full-scan implementation. The current [recovery validation model](recovery-row-hash-proofs.md) reuses committed snapshot integrity, validates changed SQLite snapshots, and supports fixed reads inside existing main transactions. Historical scan counts are not the current contract.

## Evidence and scope

The supplied 52.785-second CPU sample contains a synchronous recall that begins
12.292 seconds into the sample and continues through its end. Its observed recall
stack occupies 38.54 seconds, a lower bound rather than the recall's total time.
The pre-mutation captures account for 30.13 seconds in `CommitmentPrivacy.capture`
and 8.405 seconds in `SemanticPrivacy.capture`. The latter is almost entirely
`partitionKey -> MemoryService.list`: about 1.441 seconds in `refreshSupport` and
6.937 seconds in the following proof/applicability read.

The root recall already owns a main SQLite transaction. Previous source/search
snapshot optimizations deliberately fell back for existing transactions, so these
capture loops repeatedly verified the entire independent recovery chain for each
nested repository/source/identity read.

This repair adds two fixed synchronous read implementations:

- Exact pre-mutation commitment reference capture.
- Authorized list/listByIds qualification, after unchanged lifecycle maintenance.

They construct read-only repository/history/authority objects internally and
accept no extension callbacks or reusable authorization objects. A nested main
savepoint preserves the caller's transaction and its uncommitted view. Both
connections are query-only during the owned read. The full chain is verified
under the authority read lock; every nested gate checks both connections'
transaction, query-only, change-counter, schema and binding invariants. The scope
is discarded, the owned savepoint/authority transaction end, write access is
restored, and a fresh full chain check runs before results escape. The caller's
outer main transaction remains intact. Recall's durable revocation and derivative
writes happen afterward, with their existing checks.

No chain rows, successful permission decisions, list results, or parsed event
schemas are cached across calls. The existing exact SHA proof optimization is
unchanged. Quarantine replay and nonstandard journal/query-only modes retain the
original fully checked fallback. Semantic `refreshSupport` remains outside the
read-only scope. No transaction crosses an await.

## Reproducible synthetic measurements

Node v24.22.1, Linux x64, AMD EPYC 9V74. Baseline is master `1bc9bb2`
(the benchmarked local source tree `03d62c8` has an identical tree). Each fixture
has 1,000 valid unrelated recovery events, alternating source and source-edit
schema payloads. Assertions have one independent source each. There are no
semantic candidates, network, model, platform or production data operations.
One warm-up is excluded; three subsequent samples are reported. Other test work
was running on the host, so these are observed ranges, not isolated p95 estimates.

| Phase                                            | Assertions | Baseline wall ms | Candidate wall ms | Full chain scans, before -> after |
| ------------------------------------------------ | ---------: | ---------------: | ----------------: | --------------------------------: |
| prepareRecall capture in caller transaction      |         24 |      1,273–1,374 |         35.6–44.0 |                          482 -> 3 |
| prepareRecall capture in caller transaction      |        100 |      6,677–7,032 |       100.9–136.8 |                        2,002 -> 3 |
| Full memory recall, unrelated source             |         24 |      1,476–1,674 |       142.9–179.4 |                         525 -> 46 |
| Semantic partitionKey including list maintenance |         24 |          882–970 |        98.3–108.4 |                         340 -> 29 |

A zero-delay timer measured similar event-loop delays. For the 24-assertion full
recall, its observed delay changed from 1,478–1,677 ms to 147–181 ms. The candidate
is still synchronous and still blocks the HTTP event loop for its synchronous
work. These results are not a production recovery guarantee or HTTP isolation.
The partition fixture exercises the actual list/maintenance path and returns no
partition because 24 declarations exceed the existing 12-declaration limit; it
does not measure an accepted semantic candidate's complete qualification.

Run matched baseline/candidate source checkouts with the same installed dependency
versions:

```sh
node --import tsx scripts/recall-capture-benchmark.ts /path/to/checkout 1000 24 3 capture
node --import tsx scripts/recall-capture-benchmark.ts /path/to/checkout 1000 100 3 capture
node --import tsx scripts/recall-capture-benchmark.ts /path/to/checkout 1000 24 3 recall
node --import tsx scripts/recall-capture-benchmark.ts /path/to/checkout 1000 24 3 partition
```

## Regression boundaries

`tests/recovery-commitment-privacy-scope.test.ts` covers constant full-scan count,
exact reference/list ordering, ID de-duplication, existing transaction visibility,
outer rollback, blocked local writes and external authority tampering, post-unlock
verification, savepoint-only rollback, caller result independence, iterator
materialization and scene/simulation isolation. A failed pre-state capture still
records the independent durable root revocation and blocks the rolled-back main
snapshot. Existing recovery, commitment, semantic, source-edit, runtime recall,
restore and authorization suites remain required.

## Remaining work

Full chain validation is still O(chain length) per independent boundary. Write
maintenance, semantic qualification beyond the measured partition, derivative
redaction and other runtime CPU work remain synchronous. This targeted repair
reduces the profiled read amplification; it does not implement a separate HTTP
process, worker-owned engine, queue backpressure or any deployment change.
