# Reversible assertion eligibility recovery

## Status and deployment decision

This implementation is staged for final validation. Normal owner startup automatically records a baseline using the configured, currently ready database before creating the Engine, providers, timers, or HTTP application. This is the selected deployment trust policy; no manual baseline step or new operator CLI is required. Existing enrollment and fail-closed recovery requirements remain unchanged.

The HTTP publication protocol needs a durable authority change whenever normal assertion writes withdraw current history authorization. Setting `status` to `invalidated` previously changed only the main database. Revision retention intentionally skips invalidated records, and an identical reactivation may leave version and deletion epoch unchanged. Therefore revision numbers cannot fence an active → invalidated → identical active cycle.

## Event contract

The external hash-chain format remains 1. The new strict event is:

```ts
{
  kind: 'assertion_eligibility',
  schemaVersion: 1,
  operationId: string, // Fresh UUID for this aggregate.
  mode: 'transition' | 'baseline',
  changes: [{
    id: string,
    incarnationHash: string,
    state: {
      status: 'active' | 'disputed' | 'superseded' | 'invalidated',
      lineageStatus: 'verified' | 'unverified' | 'quarantined' | null
    },
    materialHash: string | null
  }]
}
```

The ordered authority sequence supplies the monotonic identity. A fresh UUID distinguishes operations, and the authority also force-appends this event kind rather than deduplicating its payload. Repeating the same withdrawal/regrant cycle therefore advances the authority each time. Successful command-idempotency replay does not perform another write or produce another event.

The event has no assertion bodies or proof bodies. `incarnationHash` covers the immutable assertion ID and original `learnedAt`. Terminally deleted IDs cannot be reused. The history deletion epoch is a redaction counter, not an incarnation identifier.

A negative has `materialHash: null`. A grant carries the SHA-256 hash of the complete stored record except `status` and `lineageStatus`, together with the complete stored support proofs. Object keys are canonicalized; array order and all proof fields remain significant. The exact target status and lineage are already in the event. Hashes qualify supplied trusted material; they are not a signature, proof of freshness, or a means to reconstruct missing content.

## Shared write boundary and cost

`MemoryRevisionCoordinator.beforeWrite` retains the first pre-write record for each touched assertion. The repository reports only immutable final status/lineage fields after serialization. The outer coordinator transaction finishes normal revision work, cheaply compares those fields, and reads the final record/proofs only for actual permission-transition candidates before committing one aggregate authority event synchronously before its main database commit. Ordinary and support-only writes add no eligibility row or proof reads. Invalidated/unretained records participate. Only changes to the invalidated or quarantined dimensions produce an eligibility obligation. Ordinary content edits, supersession, temporal applicability and normal creation do not become global restore barriers.

Revision checkpoints remain available for guarded commands and derivative privacy work. They do not flush eligibility or split the aggregate. Nested rollback restores the original map. A main rollback after a durable external intent leaves the authority/main mismatch in place, blocking further live reads and writes until offline reconciliation. An unrelated source or other domain event is never considered a substitute for this typed obligation.

The new work is proportional to the transaction's touched assertions. There is no new graph traversal, full-store scan during ordinary writes, per-derived-row authority commit, retry loop, timeout, or protective cap. A batch of 16 changed assertions with 16 revision checkpoints is tested to append one eligibility event. A warm 64-transaction/512-write test confirms only the existing first-before and revision-flush row reads, zero additional eligibility row reads, and zero authority commits for ordinary writes.

## Restore semantics

Recovery selects the latest eligibility obligation for each assertion, then applies it at its original ordered chain position only if it is later than the snapshot's acknowledged head. This avoids replaying an obsolete grant when a later negative already settles the final state. It also lets a later source intent withdraw an earlier regrant, including when the source operation's main transaction rolled back before derivative changes were journaled.

- A negative sets only its negative dimensions: invalidated and/or quarantined. It never clears a stricter existing withdrawal in the other dimension.
- Negative replay preserves retained history, content, proofs, fingerprint, assertion version and deletion epoch. Existing destructive source/history events keep their original semantics and may separately erase bodies or advance epochs.
- A grant for an existing assertion requires the same incarnation and exact trusted non-eligibility material/proofs. Missing or different material leaves restoration quarantined. An older backup cannot manufacture reviewed content or cleared/replaced proofs from an event hash.
- An entirely absent assertion stays absent, even for a grant. This permits restoration of backups predating that assertion's creation without recreating roots or making orphaned history readable. Independent dependent-domain checks remain in force.
- Terminal assertion deletion dominates eligibility events. It is never bypassed by a matching grant hash or a later fabricated reactivation.
- An already-acknowledged grant does not require its old material hash to match a later legitimate content edit in the snapshot.
- A matching regrant after a nonterminal purge does not restore pre-purge history or reset an epoch. Existing replay can conservatively advance the redaction counter further.

## Automatic upgrade baseline

The selected policy trusts the database configured at first normal runtime startup as current. After the existing recovery gate passes, `ensureAssertionEligibilityBaseline(db)` acquires a main `BEGIN IMMEDIATE` transaction, checks the verified authority for a prior baseline, and records the current negative census in one body-free event, including an empty census. The same transaction serializes the check and census with main writers. Restart is idempotent. An earlier transition-only prototype database also gets a census; a transition is not evidence that all legacy negatives were covered.

The baseline precedes Engine construction and therefore provider, scheduler, and request work. Readonly publication workers, backup reads, bare Store construction, and offline restore maintenance do not migrate. The in-memory development exception has no durable baseline. The explicit offline primitive remains available to synthetic recovery fixtures and rejects an already-started eligibility journal.

SQLite cannot prove that the configured database is not an old backup. This selected policy accepts that trust assumption; it does not discover other database copies or reconstruct unjournaled historical states. A stale acknowledged head, quarantine, invalid chain, or unsupported reader still blocks startup. A durable baseline append followed by main rollback preserves the mismatch and requires normal offline reconciliation, never automatic retry, gate reset, or reenrollment.

Replay applies recorded negatives to a pre-baseline backup without requiring that backup to contain the latest global data or all newly created assertions. A baseline does not invent unjournaled past regrants: if an old backup is negative while the current census is empty, the restored assertion remains negative. Subsequent regrants use the normal exact-material rules.

## Reader compatibility

New typed events require reader capability 11. New readers retain parsing of old format-1 events and old snapshots. Main capability is raised when an eligibility event or startup baseline is recorded. Normal persistent runtime startup therefore requires reader 11, even for an empty census; constructing a bare Store or memory service does not itself upgrade it.

Reader 10 cannot open a main database whose minimum reader is 11. If a main rollback leaves its old capability/head while the external event is durable, reader 10 also fails parsing the unknown event. There is no direct downgrade after the new event is emitted. Do not delete the event, reset the gate, replace the authority, or reenroll a backup to downgrade.

## Validation boundaries

Focused tests cover strict body-free events, exact ABA with unchanged history/epoch, conservative negative replay, matching/missing grant material, old baselines, command idempotency, aggregation and nested rollback. Independent review tests cover terminal dominance, source intent surviving rollback, acknowledged grants followed by edits, absent roots, and purge/regrant history preservation. Separate actual Node HTTP interleaving tests exercise the owner worker and readonly publication actor. Startup tests also cover the current negative census, empty and transition-only migration, restart idempotence, and a durable event surviving main rollback. Full project checks/build remain separate release requirements.
