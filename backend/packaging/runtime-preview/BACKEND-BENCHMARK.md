# Read-only HTTP snapshot benchmark
This driver does not start either backend, seed data, modify settings, contact
external ports, or deploy. Run it only inside the network namespace of a
**disposable synthetic** rehearsal, with a generated admin token held in
`HUMANENGINE_ADMIN_TOKEN`. The HTTP client always uses literal 127.0.0.1:
no DNS names, redirects, browser, external providers, or publisher changes.
The output includes no token, bodies, settings values, or request headers.

Use the original TypeScript runtime pinned by MIGRATION-DRILL.md and an exact Go
preview. Seed two synthetic group history rows through the existing SnowLuma
`recall_fixture.cjs` initial phase using one stable epoch and its private action
peer. Do **not** send recall events or use production data in this benchmark.
Record the clean snapshot revision and matching independent authority. Restore
a fresh matched disposable pair per engine leg; never attach two engines to the
same pair. Preserve protected configuration and matching authority. SnowLuma
uses the same event port in both replicas, so prefer **one active owner per leg**.
The automation/host wrapper is not included in this change.

In the already-open engine namespace for each AB/BA leg (TS, Go, Go, TS):
```sh
# environment already contains a synthetic HUMANENGINE_ADMIN_TOKEN
python3 backend/packaging/runtime-preview/backend_benchmark.py \
  --single ts --ts-port 3000 --fixture-timestamp "$SYNTHETIC_EPOCH" \
  --warmup 2 --window 20 --rate 6 --timeout 3
# use --single go --go-port 3001 for Go
```
Compare `settings_sha256` exactly across all four successful receipts with the
same fixture and scheduling parameters. Missing/failed receipts or mismatched
digests invalidate the comparison; report exact runtimes/fixture and runner
alongside the timing. Omitting `--single` orchestrates AB/BA timing against two
already-opened loopback ports, primarily for stubs. Never run concurrent writers
or conflicting connector listeners to use that mode.

Each owner preflight checks one 401 and exact protected settings subset, plus
the peer's ordered canonical IDs/text/status/origin/UTC timestamps and history
pagination. Only dynamic row receipt times are excluded from equality (but
are required to be canonical UTC). Every timed body is revalidated. Each
route has two keep-alive lanes at fixed target rate, with a separate warmup
and bounded drain. Missed ticks are reported, never replayed. Counts distinguish
in-window successes from completed, valid tail responses. Latency uses
monotonic full HTTP body completion, excludes JSON validation, and reports
nearest-rank p50/p95/p99 for valid successful requests only. Report errors,
status counts, sample counts and missed ticks alongside latency. No throughput
threshold is asserted; the target is not capacity. This does not measure CPU,
RSS, model work, local-history latency, or production load.

Local driver tests exercise loopback **stubs** only:
`python3 -B -m unittest test_backend_benchmark -v`.
No stub receipt is a real Go/TypeScript comparison.

## Paired disposable packages

On a local Linux Docker Engine with cgroup v2, build the same three
revision-labelled images as the package migration workflow (candidate runtime,
pinned TypeScript runtime and pinned TypeScript build stage). Then run:

    python3 backend/packaging/runtime-preview/packaged_benchmark.py \
      --ts-image humanengine-drill-ts:local --ts-tools-image humanengine-drill-ts-tools:local \
      --go-image humanengine-go-preview:local --go-revision "$(git rev-parse HEAD)" \
      --evidence /tmp/humanengine-paired-http.json

The runner initializes a signed two-row SnowLuma seed with pinned TypeScript,
cleanly stops it and checks the matching data/authority head. Each TS/Go/Go/TS
window opens a fresh offline copy of that entire pair, with its own Docker
volume and one live owner at a time. The peer uses the Docker none network, and
the owners and real benchmark driver share only its private loopback. No host
port, sudo, external platform or restore operation is needed. The source seed
volume is mounted read-only during copies. Setup, cold start, connector
readiness, projection checks outside individual samples and cleanup are not
latency samples.

Both owners use the same CPU bandwidth quota (default: two; set
`--cpus 1`, `--cpus 2` or `--cpus 4`), a 1536 MiB memory limit and no Docker
healthcheck. Compare separate four-leg trials on the same runner, with enough
effective CPU capacity for the quotas being compared. The evidence records
`cpu.max`, the owner process leader's actual CPU affinity, and, for Go only,
the effective `GOMAXPROCS` reported at startup. A one-CPU quota does not
necessarily yield `GOMAXPROCS=1`; the baseline does not override it.

Owner cgroup CPU deltas (including total and throttled periods and
throttled microseconds) cover the driver invocation including preflight and
warmup. Throttling is resource context, not proof of the cause of request
latency. `memory.peak` includes boot, page cache, and owners in the same
cgroup; it is not process RSS and excludes the separate fixture peer. These
are not allocation or lock counters. The two GETs, row parity, cgroup and timing
evidence do not qualify the broader mixed-load, recovery or cutover gates.
This runner is explicit opt-in; the package workflow does not call it.
