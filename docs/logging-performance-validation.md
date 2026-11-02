# Logging performance validation — 2026-11-02

## Scope and method

This is bounded, synthetic local validation, not a production capacity claim. The final comparison used two snapshots of the same `b6a08ae` application source; only the reviewed logging change differed. Both ran the actual Engine receive hooks with reply and learning disabled: 500 text messages, 100 scenes, 250 recovery entries, a 5,000-row synthetic meme catalog, and concurrent local authenticated HTTP requests. Model calls were forbidden and remained zero. Workers were warmed before measurement. Total process CPU includes worker CPU; elapsed time includes final draining, file synchronization and shutdown accounting. No production messages, credentials or media were used.

Each scenario has **one matched pair**, not enough repetitions to establish a small CPU difference statistically. The settings endpoint had only 13 observations per run, so its reported p95 is also its maximum and is sensitive to cold requests. This is local HTTP responsiveness, not browser rendering or interactive navigation validation.

## Final actual-Engine results

| Scenario                                  |                  Baseline → worker total CPU (ms) | Baseline → worker elapsed (ms) |        Event-loop p95 (ms) | Settings HTTP p50 / p95 (ms) |              Peak RSS (MiB) |
| ----------------------------------------- | ------------------------------------------------: | -----------------------------: | -------------------------: | ---------------------------- | --------------------------: |
| Normal stdout reader                      |                                     5,809 → 5,135 |                  5,639 → 4,960 |                83.1 → 64.5 | 102.5 / 422.4 → 95.3 / 201.9 |               196.4 → 240.1 |
| Stdout reader paused for 100 ms           |                                     5,725 → 6,040 |                  5,579 → 5,780 |                77.0 → 76.9 | 98.3 / 293.1 → 110.1 / 210.3 |               190.6 → 237.8 |
| Stdout reader paused for 2 s (saturation) | **Not a throughput comparison: delivery differs** |                   Not compared | Not used for a speed claim | Not used for a speed claim   | approximately 200.8 → 236.0 |

Normal and 100 ms cases each delivered all **3,050 records to each sink**, with file durability confirmed and zero rejected, failed, unconfirmed or pending records at completion. Actual Engine log duration/UUID fields vary, so these are equal input/record-count comparisons, not byte-identical output: normal file bytes were 1,643,386 versus 1,643,286; stdout bytes were 2,142,379 versus 2,142,229.

In the 2 s saturation case, both file sinks completed and synchronized all 3,050 records. The worker stdout sink accepted and wrote 2,374 records and explicitly rejected **676 whole records** (480,103 bytes) as queue-full; it reported zero failed/unconfirmed/pending records and incomplete overall shutdown delivery. The baseline buffered and delivered all 3,050 stdout records. The observed worker stdout pending-byte maximum was 159,047 bytes versus a baseline stdout buffer maximum of 605,378 bytes. These are observed counters in this workload, not interchangeable definitions or configured memory ceilings. **Dropped work is not credited as a performance improvement.**

Conclusion: the final normal case improved responsiveness and total CPU, but the 100 ms case used about 5.5% more CPU and did not improve median HTTP latency. Worker isolation adds roughly 44–47 MiB peak RSS here. This is a bounded responsiveness/backpressure tradeoff, **not a universal CPU reduction or a lossless logging guarantee**. Queue overflow must remain visible in health reporting.

## Component investigation and corrections

The component workload used 50 warm-up records followed by 2,000 measured records, the same formatting, file and stdout sinks, rotation, per-batch file synchronization, stdout completion and final durable close. Corrected matched runs verified identical ordered record IDs and output hashes: 2,050 total records per sink, 5,273,540 total file bytes and 7,901,640 stdout bytes. No rejected or incomplete run was treated as a speed win.

An initial exploratory harness inherited a TypeScript loader in workers and omitted a baseline rotation synchronization point. Those results were superseded by plain-Node bundled runs with synchronization parity. The corrected old worker still regressed: three-pair median total CPU was 144.2 ms baseline versus 320.1 ms worker, and elapsed time 133.7 versus 549.1 ms. The loader was not an explanation for that regression.

Separate main/worker CPU profiles identified per-record message dispatch, UTF-8 encoding/allocation, and polling-based flush waits as contributors. Bounded message batching and notification-based acknowledgements reduced those costs. An intermediate, corrected three-pair component comparison measured total CPU 188.6 → 208.0 ms, elapsed 173.1 → 143.2 ms, maximum loop gap 17.4 → 8.6 ms, and RSS 68.5 → 107.2 MiB. CPU ranges overlapped; this was not a CPU win. The final native Buffer encoding and deadline fixes were subsequently assessed in the actual-Engine cases above, rather than relabeling the intermediate component result as final.

A copied test-only writer injected a 2 ms delay per file write: with 201 equal, durable records, the baseline heartbeat gap was approximately 438 ms versus 5 ms for the worker, while total elapsed remained approximately 443 versus 441 ms. This demonstrates isolation under **simulated** slow writes, not measured physical slow-disk performance.

Slow-pipe testing also exposed an `EAGAIN` failure in the earlier worker. The corrected implementation retries only the unwritten suffix for that retryable condition and reports backpressure. Early failed/incomplete stdout runs were excluded. An initial capture driver also waited for process exit rather than complete pipe closure; corrected runs waited for pipe closure and drain before checking output.

## Limits and harness safety

- Synthetic text-only ingress does not establish image, model-provider or production storage throughput.
- CPU profiles sampled the investigated JavaScript threads; they do not alone account for all native/helper-thread CPU. Total process CPU, rather than sampled wall-time percentages, is used for comparisons.
- Watchdog time and RSS checks are cooperative sampling, not hard OS resource limits; a blocked loop can delay checks.
- Benchmark state and log directories are disposable and isolated from inherited production paths. The database is under a `data` child directory while recovery authority is a sibling, preserving the authority-independence guard.
- An earlier generic cleanup test failed at setup and did **not** prove HTTP-stage cleanup. That claim was withdrawn. The corrected fault test reached a real HTTP 200 response, deliberately asserted 999, verified that exact assertion failure, then verified temporary-directory cleanup, environment restoration and preservation of preexisting sentinel data.
- Real browser interaction and physical slow-disk behavior remain outside this evidence. No further repetitive benchmark runs were used to select a favorable result.
