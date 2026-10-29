# Minimal migration and evaluation checklist

This checklist does not authorize deployment, paid calls or real platform sends.
Use a separate test installation first. See [operations](operations.md),
[recovery authority](recovery-authority.md), and [evaluation](memory-real-model-evaluation.md)
for the full boundaries.

1. **Identify the current live installation.** Stop it before migration/enrollment.
   Retain its code version, data, referenced media and secret installation key in
   restricted encrypted storage. Do not print credentials or include them in a source
   archive. A backup containing ciphertext plus the key can decrypt credentials.
2. **Retain independent authority custody.** New empty stores enroll automatically.
   A pre-gate nonempty live database requires the documented deliberate offline
   `scripts/recovery-authority.ts enroll DATABASE AUTHORITY_DIRECTORY --current-live-database-not-a-backup`
   command. Never enroll an old snapshot or replace the independent current head
   with a matching older copy. Missing trustworthy authority is a stop condition.
3. **Make and verify a supported backup.** Use
   `node --import tsx scripts/backup.ts DATABASE MEDIA_ROOT NEW_BACKUP_DIRECTORY`.
   Destination must not exist. Do not copy an active SQLite file without its WAL
   protocol. Keep the independent authority separately; the backup is not its freshness proof.
4. **Restore only to a new destination.** Use
   `node --import tsx scripts/restore.ts BACKUP_DIRECTORY NEW_DATA_DIRECTORY AUTHORITY_DIRECTORY`.
   On quarantine, unknown format, missing newer grant/pin/plan material or credential
   revision mismatch, stop. Preserve the source and obtain compatible newer material;
   do not edit the gate/head or use an older reader to get around the error.
5. **Inspect before any connection.** Review restored enabled plugins/adapters and
   their executable sources, model routes, scene participation/learning, memory-read/personalization controls and credentials.
   Read controls default ON for legacy scenes; pause is independent of learning and
   does not delete records. This release requires participating recovery reader capability 10;
   older binaries must not be used to bypass a newer authority requirement.
   Keep real adapters disabled for evaluation. Restore does not itself start services,
   reconnect a platform, replay old chats or resend pending/unknown operations.
6. **Keep cost uncertainty visible.** Every restore creates a fresh model HOLD,
   including with budget enforcement OFF. Management and non-model reads can continue.
   The authenticated settings review acknowledges the exact current restoration only
   after an administrator's review; it does not reconstruct bills or release unresolved
   attempts. External tool/storage/compute receipts retain their actual billing dates and
   unresolved coverage across days; administrator corrections are not invoice verification.
   Leave HOLD in place until explicitly ready for new authorized test calls.
7. **Run deterministic smoke checks, then optional API tests.** Verify a synthetic
   self/third-person record, scene isolation, a recalled source, a two-person changed
   plan, and restore/pin behavior. Experiment switches default OFF; enable one at a time.
   Follow the separate spending/semantic evaluation protocol only after API authorization.
8. **Rollback conservatively.** Preserve prior source checkpoints, but old binaries may
   not understand new epochs, source events, pin policy or authority gates. An old data
   snapshot is not safe merely because it opens. Use the participating restore flow and
   current independent authority; do not overwrite a live database or discard newer deletion
   obligations. If the required newer material is unavailable, remain stopped/quarantined.

A successful restore preserves the supported recorded safety state of snapshot material;
it does not recreate later general learning, complete confirmation timelines, raw chat,
usage or complete financial history. Safe external-cost receipt metadata and retained
recorded-plan transitions are supported within their explicit bounded contracts; missing
new transition material still causes quarantine. See [operational history and retention](memory-operational-history.md)
for registered local backup expiry (dry-run by default, never third-party deletion).
No real-platform readiness or semantic quality is inferred
from the offline tests.
