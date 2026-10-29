# Recovery authority (implemented safety boundary)

A database backup is not evidence that no deletions happened afterward. Keep the independent authority directory outside the data/backup directories. Its metadata contains stable IDs, hashes and revocation references and is private, not anonymous. Protect it with the same care as the database. Loss or rollback of this independently retained authority cannot be detected from a matching old copy alone; there is deliberately no recovery override claiming otherwise.

New empty persistent stores enroll automatically. Existing nonempty stores require a deliberate offline one-time enrollment of the **current live database**, never an old backup:

```
node --import tsx scripts/recovery-authority.ts enroll DATABASE AUTHORITY_DIRECTORY --current-live-database-not-a-backup
```

Enrollment is an operator trust decision: it cannot reconstruct revocations already lost before enrollment. The default authority is a sibling of the data directory under `.humanengine-authority/<database-path-hash>`; it is never stored inside the data directory. An explicit new-install path may be configured with `HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY`. Restore uses the original authority path bound in the database; relocation custody transfer is not implemented.

Export a metadata-only transport record independently:

```
node --import tsx scripts/recovery-authority.ts export DATABASE NEW_EXPORT_FILE
```

An exported file is not proof of freshness. Current restore requires access to the original independently retained authority directory:

```
node --import tsx scripts/restore.ts BACKUP_DIRECTORY NEW_DATA_DIRECTORY AUTHORITY_DIRECTORY
```

The source backup is unchanged. Missing authority, a copied authority at another path, mismatched instance/chain prefix, unknown format, unsupported dependent subsystem, failed original media hash or concurrent head changes fail closed. A private `.quarantine` staging directory is not a running instance. Restore validates original bytes and uses the actual memory lifecycle to replay revocations, without models, network calls or chat ingestion. There is no automatic startup/send after restore.

Revocation intent is written durably to the authority **before** local mutation. A local rollback can therefore conservatively revoke more than the attempted mutation; it cannot silently forget revocation. A head mismatch blocks current memory reads and runtime send eligibility as well as restart. Reconcile offline before returning to service. Backup itself is refused while that mismatch exists.

Terminal deletion remains terminal. Nonterminal history redaction reruns the existing history/dependent purge, not terminal assertion deletion. Source recall then recomputes current eligibility and preserves independent valid OR proofs, matching live semantics. A snapshot with a verified already-applied contiguous authority prefix can retain later authorized revisions. Unknown history-redaction intent cannot be skipped merely because a local version number is larger.

Current/future participating readers reject unsupported recovery format before migrations/secrets. Historical binaries that never implemented the gate cannot be made safe by a new metadata field: do not start an old binary on restored data. This protocol does not claim complete secret erasure, external-provider deletion, or authority rollback protection without independent custody. Unknown dependent-domain tables, columns and event versions block restoration; supported domain events use the actual service lifecycle ports described below.

## Connected recovery lifecycle ports

Offline replay connects the real MemoryService, HabitService, MemeService and ExperienceService; it does not use mocked purge acknowledgements or run learning. Source recall removes exact support, feedback/reference eligibility and dependent experience qualification according to the live lifecycle. Independently valid OR proofs, other scenes and independently supported media remain. A source-less audit/tombstone record or original bytes may remain where the live recall policy retains them; recall is not an unconditional physical deletion.

Typed habit deletion, meme deletion and final safety states, and experience deletion/final states are journaled separately. Exact qualification hashes and validation receipts prevent a later grant from being applied to different or missing evidence. Meme deletion removes only its exact scoped media files, and its durable pending intent blocks live access until filesystem and database effects finish. The restored instance is marked ready only after actual domain verification and filesystem completion. Unknown table names/columns or event versions fail closed rather than silently ignoring future data.

Tests use real temporary SQLite stores and services to compare retained-record eligibility, source removal, terminal content deletion, media isolation, policy versions and the final authority prefix. Standalone historical prototype tests alone are not evidence of this multi-domain coverage.

## Configuration and credential authority

The original v1 journal stores final booleans for ten gates: cache retention, model I/O debugging, and the eight memory projection/graph/experience feature gates and each existing scene's learning, participation and proactive flags. Later flag-schema versions add the explicitly named opt-in gates below without changing old records. Scene identity is bound by a hash of its exact canonical adapter/account/platform/kind identity. Only the unapplied ordered suffix is replayed; missing or mismatched scenes require a newer compatible snapshot. This does not recreate post-backup scenes or silently turn learning on.

Credential or plugin configuration changes record a random opaque security revision, never the credential, endpoint, configuration body or a guessable secret hash. Model security projection covers provider identity/protocol/endpoint/environment credential reference, model identity/provider/API name/capabilities and canonical role routes; cosmetic names, prices and timeouts do not change this revision. Plugin configuration and local executable-plugin authorization list changes conservatively advance it. The full budget policy (enabled/mode, monetary/token/call/output caps and allocation ratios) also advances this opaque revision on change, so an old snapshot cannot silently restore a disabled or looser budget policy. Before replay, the snapshot's security revision must exactly match the latest authority revision. An old backup with obsolete credentials/configuration is quarantined and requires newer trusted backup material: replay cannot manufacture the new secret or mark the old secret as current. Ordinary persona/name settings and usage records do not advance security revision.

## Slice8 state replay semantics

The target is current revocation and usability of material retained by the chosen snapshot, not reconstruction of messages, images or validation receipts created after it. Domain state events already in the verified applied prefix are not replayed: a deterministic media ID may have been legitimately re-admitted from a new source after an older deletion. Unapplied state events are ordered, including explicit grants, and require exact qualification hashes/receipts. Qualification checks also apply to terminal meme deletion: if source/analysis metadata for the same ID changed since the chosen snapshot, the old generation cannot be assumed equivalent. That mismatch requires a newer compatible snapshot, just as a later grant needing absent evidence does. Recovery stays quarantined in either case; it never manufactures missing images or grants based on a bare journal boolean. Source recalls remain monotonic and can be reapplied; terminal memory UUID deletions remain terminal.

Async media deletion creates a durable external intent before any await. The local head stays behind until filesystem work and synchronous local effects finish under the commit ticket's authority CAS. While pending, normal reads and send eligibility are blocked. A failure leaves recoverable quarantine, not an acknowledged completion. Current live multi-domain source recall is one outer synchronous transaction; a late domain failure rolls back local tombstones/head and leaves the external root intent as a blocking recovery obligation.

## Trust and migration boundary

The hash chain proves that a declared prefix belongs to the retained authority. It does not cryptographically attest that a maliciously rewritten backup business row truly applied that prefix. This protocol assumes trusted backups, participating binaries, and independently retained current authority custody. It does not defend against a local attacker rewriting the SQLite gate, business data and manifest together; no snapshot signature/seal is claimed. Arbitrary historical binaries cannot be forced to respect a new gate.

An identity-less snapshot from before enrollment is not silently upgraded by restore. Enroll only the current live database offline, retain its independent authority, then create a new supported backup. Never enroll an old snapshot to pretend that it is the latest live instance. If the original authority or trustworthy lineage is missing, recovery stays quarantined; a copied matching old head does not solve that uncertainty. CLI errors show bounded technical reason codes and operational next steps, never dump ledger contents, source text, secrets, or credential paths.

The CLI checks the authority again after publishing its newly created destination and before returning success. If that check fails, it removes only that invocation's published target. This is not a claim that directory reservation plus rename is a cross-process no-replace atomic primitive, nor that authority can never change after a successful return; every subsequent participating Store open and live operation still checks the current authority.

This safety journal is not a replication log for every soft negative, ranking, favorite or lock preference. Admin authentication tokens supplied through the external environment and in-memory sessions are not backed up here; rollback of that external environment is outside this protocol.

## Container deployment custody

The provided Compose configuration retains both the data volume and a separate `humanengine-authority` volume mounted at `/app/authority`, with `HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY` set consistently. Keep both volumes across upgrades; preserving only `/app/data` is insufficient. Do not place authority inside a backup payload or replace it with an older copied volume to clear quarantine. Existing databases require deliberate offline enrollment using the same absolute authority path that the application will use.

The enrollment/export/restore commands above run from the source checkout with its Node dependencies installed. The production runtime image does not ship the TypeScript maintenance scripts or `tsx`, and no separate maintenance image is supplied here. Do not assume a `docker exec` command for those scripts is available. The container wiring has static configuration tests; this is not evidence that a real deployment, volume migration or operator restore has been exercised.

## Model admission after every restore

Recovery cannot infer model/embedding costs incurred after the chosen snapshot. Every successful restore therefore writes a fresh persistent model-admission hold before marking data recovery ready, even when the copied backup already contained an older acknowledgement or budget enforcement is disabled. The hold survives restarts and date changes. Management and non-model reads remain available, but model and embedding admission require an authenticated administrator to explicitly review the uncertainty and acknowledge that specific restoration. This acknowledgement does not reconstruct missing charges, reconcile balances or certify accounting completeness; it only authorizes subsequent admission under the current policy. Restoring again creates a new hold and requires a new acknowledgement.

The authenticated settings page exposes the hold independently of the budget experiment switch. Its review checkbox and confirmation submit the exact current restoration ID to `POST /api/budget/recovery/acknowledge`; `GET /api/budget/recovery` exposes the bounded state. Acknowledgement retains `restorationCoverage=manually_acknowledged`, not a reconciled or complete label. It does not reset unresolved budget attempts or import missing consumption. No trusted consumption-ledger import workflow is provided in this slice.

The admission hold covers participating Store-backed Engine/Runtime requests, embedding work and model probes. It does not revoke a request already transmitted, and is not a sandbox for arbitrary trusted plugin code or a standalone Router instantiated without a Store. Recovery management remains usable without issuing a model request.

## Source-event revision recovery (Slice 9a)

Accepted trusted edits append a metadata-only `source_edit_head` witness before changing the local event head. It binds canonical identity, exact event/revision/deletion epoch and the event module's qualification hash (including its ordering mode). The ledger never stores source text, media locators or an edit payload to replay. Plain duplicate ingress is not an edit. Event metadata remains separate from permission to retain source bodies; the initial module retains no source body.

Restore checks the latest witness against both the snapshot's actual event head and exact current revision metadata. It never overwrites an old head with journal values or promotes an old proof to a newer source revision. Missing or different qualified material keeps recovery quarantined. A genuine later same-scene/message source recall uses the actual event lifecycle to erase all revisions/edit keys, preserves independent sources, and discharges the earlier edit obligation. Unknown source-event tables, columns or schema fields fail closed.

This restriction includes a liveness boundary: if the external edit intent became durable but the local new metadata transaction failed, the new witness's matching material may never have been committed. Merely running reconcile again cannot manufacture it or safely clear the gap. Recovery then requires trustworthy matching source material through a separately validated repair procedure, or a separately authorized source-revocation repair. No general-purpose repair command for either is provided by this slice; retain quarantine rather than directly editing/acknowledging the head. A newer backup is sufficient only when it genuinely contains the required material.

Reader capability 2 is independent of authority chain format 1. Old enrolled databases remain readable without migration until an actual source-event mutation; that mutation raises the local minimum reader in the same transaction. A reader that implements only capability 1 must reject such data. This does not enforce cooperation from arbitrary older software. Configuration authority v1 retains its original ten-flag shape; v2 adds `sourceEventRevisionsEnabled` without invalidating existing v1 chain records. Disabling enrollment does not authorize ignoring edits to already enrolled sources or restoring their previous applicability.

## Commitment recovery (Slice 9b integration)

Commitment authority uses a separate metadata-only head witness: canonical scene/entity ID, terms version, mutation version, deletion epoch and the commitment module's canonical qualification hash of its complete retained snapshot. That hash includes the head's last activated terms and terminal-state markers, plus all retained revision/evidence groups. Proposing, amending, participant confirmation, completion, cancellation and support requalification are not interchangeable; none of their terms or confirmation evidence is copied into the external journal.

Restore uses the real MemoryService commitment authority/lifecycle rather than a permissive substitute. A latest head must match actual retained terms and evidence after privacy replay. Missing later amendments or confirmations, including a previously activated 19:00 plan whose confirmation was withdrawn, cannot be replaced by an older confirmed 18:00 plan. A mismatch remains quarantined; the journal does not supply missing participants' consent. Terminal entity deletion is replayed independently and clears all revision/evidence bodies and command receipts while retaining only the established denial metadata. Source recall and terminal assertion deletion use the actual proof lifecycle and preserve independently authorized alternatives.

Reader capability 3 is required by the first actual commitment mutation. Source-event-only mutations still require capability 2; the external authority chain format remains 1. Configuration authority v3 adds exactly three opt-in flags—commitment admission, commitment answer/tool exposure, and the new restricted historical-read tool—while v1's ten flags and v2's eleven flags remain strictly readable. These settings do not enable learning or automatic messaging.

As with source edits, a durable new commitment witness followed by a failed local transaction may leave no matching committed material. Quarantine is a safe failure, not proof of an automatically repairable state. Recovery does not invent later terms, confirmations or missing accounting history; the independent model-admission hold remains required after every successful restore.

A root source-recall lifecycle can itself execute the historical redactions that also appear as later child entries in the authority. During offline recovery only, the implementation captures complete typed revocation effects inside one successful synchronous SQLite transaction. It consumes an exact matching nonterminal assertion receipt once instead of redacting the same history again and advancing its backing epoch a second time. Failed transactions publish no receipts; asynchronous capture is rejected. This is not a waiver based on a snapshot's higher version and not a general permission to skip unknown or terminal events. Tests compare live and restored backing versions/epochs, retained commitment qualification and the actual current projection, including repeated restore.

### Scene model-read controls (reader capability 5)

Scene safety events have a distinct `scene_flags` version 2 carrying the legacy
participation/learning/proactive booleans plus `memoryReadEnabled` and
`personalizationEnabled`. Version 1 remains strictly readable with its original
three fields. Legacy absent read flags mean enabled; malformed nonboolean stored
flags fail closed in model-read authorization. Server-owned read-policy revisions
advance for control or canonical routing-identity changes, not display renames.
Ordered replay preserves the same policy transitions and does not reopen OFF via
an old client that omits new fields. Persisting the new policy requires reader 5;
the independent authority-chain format remains 1. This gate is not a claim that
an arbitrary old or modified binary cooperates.
