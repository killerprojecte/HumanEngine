# Bounded offline read-path measurements

Date: 2026-10-29. Synthetic local data only; no real model, embedding or platform calls.

Environment: v24.22.1, Linux x64, AMD EPYC 9V74 80-Core Processor, SQLite 3.53.4. Three sequential samples per operation; raw ranges are not production p95.

## Completed final samples

Flat manually created assertions across ten people; no deep proof DAG. Graph shadow ON has **zero graph edges**, not a populated graph capacity test. Each process had 90 seconds wall time, 75 CPU seconds, a 256 MiB V8 heap, and an 800 MiB sampled RSS stop threshold (100 ms sampling). Heap limit is not RSS limit. No production forced garbage collection.

| Records | Operation                            | Elapsed range (ms) | Results | Serialized result UTF-8 bytes |
| ------: | ------------------------------------ | -----------------: | ------: | ----------------------------: |
|    1000 | authorized-list                      |     77.268–116.222 |    1000 |                        397391 |
|    1000 | exact-person-profile                 |       9.323–12.379 |     100 |                         54679 |
|    1000 | lexical-search-graph-off             |    221.418–235.647 |      20 |                          8211 |
|    1000 | lexical-search-empty-graph-shadow-on |    283.249–293.898 |      20 |                          8211 |
|    1000 | working-set-candidate-bound          |    229.577–263.316 |      12 |                         12430 |
|    1000 | independent-experience-view          |    215.767–260.766 |      12 |                          8463 |
|    2000 | authorized-list                      |    156.276–201.138 |    2000 |                        795891 |
|    2000 | exact-person-profile                 |      18.173–21.410 |     200 |                        109379 |
|    2000 | lexical-search-graph-off             |    526.332–544.720 |      20 |                          8231 |
|    2000 | lexical-search-empty-graph-shadow-on |    584.994–604.280 |      20 |                          8231 |
|    2000 | working-set-candidate-bound          |    428.594–544.364 |      12 |                         12442 |
|    2000 | independent-experience-view          |    391.863–506.836 |      12 |                          8475 |

| Records | Whole process (s) | Sampled max RSS (KiB) | DB+WAL+SHM before / seeded / after reads (bytes) |
| ------: | ----------------: | --------------------: | ------------------------------------------------ |
|    1000 |             6.426 |               416,720 | 790,832 / 2,838,472 / 3,114,512                  |
|    2000 |            11.746 |               554,668 | 790,832 / 9,219,224 / 9,219,224                  |

Both processes exited 0 with modelCalls=0 and networkCalls=0 (throwing traps). SQLite size excludes the separately retained authority database. This finite sample demonstrates completion within these measured bounds, not unlimited long-term capacity.

## Failures retained, then diagnosed

- Initial unbatched 1k attempt exited 137 without complete output; cause was not established. It is not labelled OOM or used as a numeric speedup baseline.
- Before fixed statement handles, the 2k attempt was explicitly stopped during seeding at the 800 MiB RSS threshold: exit -9, 5.574 s, sampled 827,976 KiB. No complete operation was reported. Its original receipt path was reused on retry; prior values are explicitly recovered from the original tool observation and contemporaneous progress report, not represented as an untouched receipt file.
- Independent 1k diagnostic processes found 43 primary prepares per seed record (linear, not evidence of O(N²) SQL counts on this flat workload). Original single transaction peaked at 567.5 MiB; chunk/yield at 276.6 MiB; the same single transaction with six fixed recovery SELECT handles reused peaked at 339.0 MiB. A diagnostic forced-GC variant was used only to investigate lifetime/allocation, never as the production fix.
- The narrow production fix reuses six compiled SELECT statements per actual database connection. Every call still executes get/all and validates current gate/head/full hash chain. It caches no rows, authorization decisions, existence result or head. External head changes, corruption, transaction rollback, schema recreation, close and a fresh connection are regression-tested.

## Exposure and burst safeguards

The actual Engine 1k integration asserts bounded full-scene authorization reads per phase and a complete 1,000-record person result (no new 40-record truncation). Same-body revision changes and recall between prepare/delivery reject old receipts. Each phase gets fresh authority; no cross-await permission cache. Working-set callbacks still perform exact-ID qualification and bounded plan-protection work, so constant list counts are not constant total CPU cost.

A separate actual Runtime 100-message fixture recorded ingress/received=100, eight scripted decision calls, zero synthetic sends, four topics and zero final active/deferred/overflow counts. This demonstrates the tested coalescing behavior, not provider usage or answer quality.

## Not demonstrated

Result JSON bytes exclude provider framing and are not tokens. No real-provider cost, savings, quality or tokenizer calibration is established. Deep proof histories, populated graphs, growth beyond 2k and sustained concurrent load remain unmeasured. Larger journal chains still undergo complete validation; no caching of validity was introduced to make these measurements pass.
