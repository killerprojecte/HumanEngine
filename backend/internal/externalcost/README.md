# External cost storage and isolated replay

Port of `src/budget/external-cost/{storage,contracts}.ts` and
`src/memory/recovery/external-cost.ts`. `Receipt` is immutable to callers;
`Parse` validates the storage contract and reconstructs Zod schema property
order before JavaScript-compatible serialization and SHA-256. This deliberately
differs from hashing raw stored JSON. `Latest`, `Correction`, `Current`, and
`Put` validate schema/material and retain source conflict, uniqueness, and
10,000-current-receipt capacity behavior. Storage itself allows revision gaps
and initial correction IDs, just like source; recovery events do not.

`ReplayOffline` is a **domain component, not a safe global recovery entrypoint**.
It requires an already-authorized caller-owned `Maintenance` implementation.
The caller must verify complete authority history and staging admission, path
independence/no symlinks, authority instance, quarantine state and supported
reader capability. Its per-receipt immediate transaction must require the
operational reader (minimumReader **10**, not commitment reader 3), update that
minimum in the same transaction, and roll back on failure. A caller already in
a transaction uses nested savepoints as in better-sqlite3. Earlier completed
receipts remain committed if a later receipt fails absent an outer transaction.
The final comparison checks every current receipt against the exact replayed
head, not merely IDs or revisions.

`NewOfflineMaintenance` now adapts a real `recovery.AdmitMaintenance` capability
for caller-owned disk staging. Admission verifies quarantine, authority identity
and prefix, and the adapter supplies guarded per-receipt transactions. The global
offline reconciliation orchestrator remains a dependency. Ordinary recovery
`Write`, diagnostic `Head`, production HTTP mutation, enrollment, ACK, and
ready-state transitions are not used here. Other event domains are validated
then ignored by this component, never certified as replayed. No restart replay.

Tests execute actual Node/Zod/better-sqlite3 source and compare checked-in
synthetic fixtures pinned to source hashes. Disk-backed tests cover both a fixture-only transaction adapter and the real
admitted recovery capability. They prove per-receipt commits, partial failure,
reader upgrade, native rollback protection and unchanged quarantined global
head. Domain success still does not certify other domains or global readiness.

Run from backend: `go test -race ./internal/externalcost` and
`go vet ./internal/externalcost`. Live differential tests need repository Node
and installed locked dependencies; production Go package does not invoke Node.
