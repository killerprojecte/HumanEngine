# Recorded plan history, local retention and external costs

These are separate operational controls. None verifies that a person really attended or completed an event, reconciles a provider invoice automatically, or erases third-party backups.

## Recorded declaration timeline

`memory.commitmentTimeline` is distinct from `memory.commitmentHistory`. The older tool projects currently retained, authorized evidence for each terms version; it is not a historical confirmation snapshot. The timeline records forward-going declaration-state transitions and their server recording times. Event occurrence dates remain separate and may be unknown.

The model must first receive the exact current plan anchor. The timeline binds that first anchor and mutation version, never upgrades it to a newer head, and returns one indivisible qualified packet. A changed head, read authorization or source invalidates an in-flight packet. History cannot become a new learning parent. The administrative plan view offers the same recorded-time query under authenticated management access.

At most 64 transitions are retained per plan; one query returns at most 20. Coverage is always limited. Legacy plans have no fabricated past transitions. A requested recording time can yield `legacy_history_unknown`, `retention_incomplete`, `recorded_clock_nonmonotonic`, `anchor_version_bound`, `result_limit` or `no_recorded_state`; these do not mean that an event did not happen.

Current privacy authority dominates historical visibility. Source withdrawal prunes protected evidence and writes a body-free recorded privacy marker. A selected redacted marker means unknown/unavailable state, not a fallback to an earlier confirmed plan. Independent surviving OR proofs can keep the current plan available. Source edits and ordinary metadata drift are not permission to erase historical bodies; privacy revocation is a separate lifecycle.

Recovery replays the original privacy marker time and head, not the recovery wall clock. A backup missing material required for a newer authorized transition is quarantined rather than silently reconstructed. This is a bounded declaration history, not full database or real-world time travel.

## Explicit local backup retention

Run from the repository using the installed dependencies:

```sh
node --import tsx scripts/backup-retention.ts init /private/control/retention.sqlite /private/managed-backups
node --import tsx scripts/backup-retention.ts register /private/control/retention.sqlite backup-name 2026-12-01T00:00:00Z
node --import tsx scripts/backup-retention.ts inventory /private/control/retention.sqlite
node --import tsx scripts/backup-retention.ts expire /private/control/retention.sqlite
# Only after reviewing the dry-run inventory:
node --import tsx scripts/backup-retention.ts expire /private/control/retention.sqlite --execute
```

The control directory must already exist. Use an exclusively managed, private local root and keep the retention registry and independent recovery authority outside it. Registration enrolls one existing backup directory, not arbitrary paths or an entire filesystem. Names, root identity, manifest, hashes, inode/type and member inventory are checked. Unknown members, links or identity drift cause refusal/unverifiable status; they are not recursively removed.

Expiry is dry-run by default. Explicit execution persists pending intent before deleting only verified regular members, then verifies absence. Restart rechecks pending work. Keep the registry for the audit trail. A verified-absent result applies only to this registered local copy; it is not secure media erasure, deletion of Library versions, snapshots, replicas or third-party copies. This is not a defense against a malicious concurrent local filesystem administrator.

New backup copies are finalized without unsigned SQLite WAL/SHM sidecars before manifest hashing. The live source database and its journal mode are unchanged. An older copy with unmanifested sidecars is not silently accepted; create a new valid backup instead of deleting unknown files by hand to force enrollment.

## External tool, storage and compute costs

Authenticated Budget settings provide an explicit receipt form and paged current-receipt list. The API is `GET/POST /api/budget/external-costs` and `POST /api/budget/external-costs/corrections`. IDs are opaque; categories are tool, storage or compute; currency is USD. A known amount is a nonnegative safe integer in nano-USD. Unknown receipts must not include an amount. Dates are valid billing/period dates, not model scenario dates. No invoice text, URLs, credentials or chat content belong in these fields.

Exact duplicate imports are idempotent. Changed contents require an explicit correction ID and expected revision; a conflict is not automatically retried. After an uncertain response, refresh and inspect the receipt before resubmitting. Current-day known amounts are added once to the aggregate budget; do not add the displayed external subtotal to that aggregate again.

Any unresolved external receipt keeps coverage incomplete across calendar changes. When budget admission is enabled it blocks new managed model admission in strict and best-effort modes. Budget OFF does not make the receipt known, reconcile it, or release a restoration HOLD. Correcting a receipt is an administrator declaration, not provider verification. Known imported totals are not proof that all real costs have been imported.

Recovery retains safe accounting metadata and revisions through the independent authority journal. It still creates a fresh model-accounting HOLD; acknowledging that HOLD permits admission but does not assert invoice reconciliation or restore missing usage history. Keep the independent authority and session ledgers; rolling them back defeats their coverage contract.

The small strict/estimated presets only populate a draft. They do not enable budget enforcement, learning, providers, background tasks or paid calls. Saving remains explicit. Strict mode needs a trusted applicable cost contract; an estimated preset is not a USD hard cap, and an output request limit is not proof of provider enforcement. Synthetic event dates must never advance/reset the real billing clock for live API tests.
