# Local backup, restore, and vector checks

## Backup

```sh
node --import tsx scripts/backup.ts data/humanengine.sqlite data/media backups/2026-10-25
```

Create the parent `backups` directory first. The final destination must not exist. `backup()` uses `better-sqlite3.backup` (SQLite online backup), not an unsafe copy of an active WAL database. It checks SQLite integrity/foreign keys, reads media references from the resulting consistent database snapshot, and copies only those content-addressed original files. Each file has a SHA-256 and byte count in `manifest.json`; the manifest records format version, creation time, and Node version. Files/directories are created with restrictive permissions. Missing, changed, or hash-mismatched media abort the backup and remove only the newly created incomplete destination. The source remains untouched.

The source database can remain open. Media originals must remain immutable during copying; coordinate any manual media deletion/maintenance first. Only persisted SQLite business data and referenced originals are included: configuration, scoped memories, derived vector spaces, habits, meme metadata, source provenance, deletion/recall tombstones, and persisted local plugin settings. Environment variables, `.env`, plugin source files, logs, in-memory chat cache, pending generation, and send state are not copied. UI-entered credentials are stored as ciphertext in the dedicated secrets table. When any exist, the matching installation key is copied separately as `.secrets.key` with owner-only permissions. The backup therefore contains everything needed to decrypt these credentials: encrypt and tightly restrict backup storage. Legacy credential environment **names** are preserved, but environment values are not read by these scripts. See [credential recovery](secrets.md). Local plugins could persist sensitive settings, so treat the complete backup as confidential; there is no automatic encryption of the complete backup archive.

Store backups outside the served application directory, restrict access, and use your own encrypted storage/retention policy. Hashes detect corruption, not a malicious party able to replace both files and manifest. Restore only trusted backups. These helpers read individual files into memory for verification; allow headroom proportional to the database file size. Test restore periodically rather than relying only on successful backup creation.

Implementation references: [SQLite Online Backup API](https://www.sqlite.org/backup.html) and [better-sqlite3 backup API](https://github.com/WiseLibs/better-sqlite3/blob/master/docs/api.md#backupdestination-options---promise).

## Restore to a new destination

```sh
node --import tsx scripts/restore.ts backups/2026-10-25 recovered-data
```

The destination must not exist, even if it is empty. The command validates all manifest paths, hashes, byte counts, database integrity, and the exact referenced-media set, and instantiates `Store` to check that the installed sqlite-vec extension loads. It creates `recovered-data/humanengine.sqlite` and `recovered-data/media/`; it never overwrites an active database. Validation failure removes only its own new partial destination. Restore helpers do not deploy, restart, reconnect adapters, or send anything.

Before using the restored data:

1. Stop HumanEngine and confirm the old instance is no longer running. Do not run two instances on the same platform account.
2. Preserve the old data directory separately. Configure the database/media paths to the newly restored files, or manually move the entire restored directory into place while stopped. Do not copy just a database over an active WAL database.
3. The restore script restores and validates stored credentials with their installation key. Restore any optional legacy credential environment values separately and review configured enabled plugins/adapters before startup. Plugin source is not bundled.
4. Start manually and inspect status/error logs. Any newly received platform events follow normal runtime rules. There is no restoration or replay of pending sends, attention windows, raw cache, or partially completed tool operations.

An older backup necessarily lacks changes and recall notifications received after its snapshot. No restore claims knowledge of missing/offline platform events. Review this limitation before reconnecting or enabling learning; never assume a stale backup is a complete current recall ledger.

## Synthetic sqlite-vec benchmark

```sh
node --import tsx scripts/vector-benchmark.ts
# Optional bounded row counts, each between 20 and 10000:
node --import tsx scripts/vector-benchmark.ts 1000 10000
```

Script version 1 creates a temporary on-disk WAL database, seeds 32-dimensional deterministic synthetic vectors through `MemoryService`, and deletes its files after completion. Two scene scopes hold 10% and 90% of the corpus. The normal materialized scoped SQL and bounded exact SQL top-k ranking execute; expected same-scene exact neighbors and exclusion of unrelated-scene results are asserted for every query. A separate worker-thread database connection updates one configuration row every 2 ms during the write-interference samples. Results include first-query latency, fresh-connection query-cold p50/p95 over five independently opened connections, warm/concurrent-write p50/p95 over 30 queries per scope, write counts/failures, seed time, and environment versions. Connection creation and schema checks are outside the measured query interval.

“First query” means the first query after seeding. Fresh-connection samples do not purge the operating-system cache; neither measurement is **OS-cache cold**. Values include bounded JSON decoding and exact hybrid SQL ranking; this is exact search, not an ANN recall benchmark. Synthetic vectors do not measure real semantic retrieval quality. One local machine run does not establish production capacity, concurrent-user throughput, or a safe corpus limit. Run this separately from heavy builds to avoid resource contention.

## Verification

`node --import tsx --test tests/backup.test.ts` exercises an open source database through backup and fresh restore, checks configuration, memory, recall tombstones, actual PNG media hashes, refusal to overwrite existing destinations, and corruption cleanup. This is a local fixture; no real platform or provider access occurs.

### Recorded local run (2026-10-25 UTC)

Command: `node --import tsx scripts/vector-benchmark.ts` (exit 0). Script version 1; Node v24.22.1, Linux x64, Intel Xeon Platinum 8573C, SQLite 3.53.4, sqlite-vec v0.1.9. All expected-neighbor and scope assertions passed. OS caches were not evicted. This shared execution host was not otherwise isolated; tail latency is noisy and these values are not performance promises.

All timings below are milliseconds. Fresh-connection percentiles use five samples; warm and concurrent-write percentiles use 30.

| Corpus | Scoped rows | First   | Connection cold p50 / p95 | Warm p50 / p95     | Concurrent writer p50 / p95 |
| ------ | ----------- | ------- | ------------------------- | ------------------ | --------------------------- |
| 1000   | 100         | 30.753  | 1.969 / 6.765             | 2.459 / 6.862      | 2.251 / 4.519               |
| 1000   | 900         | 15.938  | 13.310 / 27.551           | 17.459 / 26.564    | 12.542 / 25.188             |
| 10000  | 1000        | 9.433   | 14.793 / 149.113          | 7.572 / 10.981     | 12.682 / 83.375             |
| 10000  | 9000        | 117.244 | 156.817 / 373.828         | 132.053 / 1012.229 | 134.039 / 828.811           |

Seed times: 1000 rows in 1437.211 ms, 10000 rows in 11007.690 ms. Concurrent writer totals: 232 writes / 0 failures, 3387 writes / 0 failures. The 9,000-row scope had a roughly one-second warm p95 in this run; do not infer a production latency target from it.

Raw captured output SHA-256: `516674ec0296ccd2c771725faef9a0f734537911e2be1355d85dbb3b83e682a7`. Measurements, not this hash alone, are the verification evidence.

## Technical logs, diagnostics, and usage

Technical logs are JSON Lines in a `logs/` sibling directory of the SQLite file, with a current file and two rotated files (about 2 MiB each). They contain identifiers, safe error codes, timing, and model IDs, not chat bodies. The dashboard keeps only the newest 500 events in memory and does not replay files after restart. Memory-only databases do not write log files.

Visible model input/output diagnostics are explicit opt-in in Settings. They contain sensitive conversation content, even after known credentials and secret-shaped fields are redacted. They exclude hidden Responses reasoning state and image bytes, expire after 15 minutes, retain at most 20 entries, and are never persisted. Disabling diagnostics, recall processing, and restart clear them. Treat authorized administration access as access to these sensitive previews; redaction is not a general personal-data anonymizer.

Usage rows aggregate observed attempts by UTC day, stable model instance, provider, API model name, and role. Configured USD-per-million rates are snapshotted at call time. Blank prices and absent provider token counters remain unknown; explicit zero prices are distinct. Failed attempts are counted without an invented cost. Provider-specific caching, reasoning-token billing, retries outside this process, canceled requests, unknown outcomes, and taxes can differ from the estimate. The provider invoice remains authoritative. No budget cap is imposed.

The model concurrency setting bounds active requests, with Planner/Thinker priority over queued background work. Work already running is not preempted. Model queues are bounded at 256, and media background work at 64 queued / 2 active. Overflow fails safely into technical logs rather than creating an unbounded backlog.

## Bounded learning recovery

Settings now expose `learningQueueMaxMessages` (per scene, default 2048; 60–20000), `learningQueueMaxTotalMessages` (all scenes, default 8192; 60–100000), `learningRetryAttempts` (including the first attempt, default 3; 1–5), and `learningRetryBaseMs` (default 1000; 100–30000 ms). Legacy saved settings acquire these defaults through schema parsing. Global admission limits may deliberately be smaller than a per-scene limit. FIFO extraction batches are at most 60 messages; overflow rejects newest admissions visibly instead of silently truncating oldest evidence.

The Settings page and authenticated `GET /api/learning/status` expose process-local message counts, batch retry attempts and sanitized failure metadata. Explicit retry uses `POST /api/learning/failures/{id}/retry`; explicit discard uses `DELETE /api/learning/failures/{id}`. Failed pre-application batches retain bounded in-memory input for operator recovery. A batch with possible partial application cannot be replayed automatically or manually. Only explicit eligible provider HTTP failures receive bounded automatic retry; authentication, malformed outputs and uncertain timeouts are not blindly repeated. Restart intentionally discards raw pending conversation input; status is not a durable outbox or a full historical audit log.
