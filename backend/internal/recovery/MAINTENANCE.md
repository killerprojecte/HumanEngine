# Offline domain maintenance

`AdmitMaintenance` accepts one exclusively caller-owned SQLite connection,
its actual file path, and an open `Authority`. The caller checks out the main
connection before admission and keeps ownership until all domain work finishes.
The capability does not close either resource.

Admission audits authority under a controlled SQLite read snapshot, checks
metadata, independent authority placement and symlinks, exact connection/file
binding, quarantine, reader compatibility, instance, and the staging prefix.
It combines TypeScript `maintenanceGate` with the **opening prefix checks** of
`reconcileRecovery`; it does not claim identical rejection phase ordering.
The actual-source test reports both source maintenance-only and combined
outcomes. Diagnostic `Authority.Head` cannot create this capability.

`Apply(ctx, minimumReader, callback)` reserves main first, then authority, in
the same order as the writer. Each call checks the authority against the
admission snapshot. External authority changes force audit and rejection of
the old capability. Unchanged snapshots reuse the existing validated stamp;
prefix lookup is indexed, not another chain scan. The authority read snapshot
is held through the effect's commit/release. Callback errors roll back that
effect and its reader upgrade, leaving prior independently committed effects.

A caller's existing main transaction is supported using a savepoint. A deferred
caller transaction is promoted to a write reservation with a zero-row gate update
before authority is acquired; this changes no gate rows and fires no row triggers.
The caller's transaction is never committed by that promotion. Gate decoding uses
exact case-sensitive keys and duplicate-last-value semantics, including when
validating live ready metadata. Reader metadata uses int64 to preserve the full
source-safe integer range on every Go architecture; capability checks reject
unsupported future readers without narrowing first. Unknown fields survive
reader upgrades.
Within a
callback, compose domains with `MaintenanceTx.Apply`, which creates a nested
savepoint without reacquiring authority. Do not recursively call the outer
`Maintenance.Apply` or another Authority method. Releasing a savepoint is not
a durable outer commit; the caller remains responsible for its transaction.

Callbacks are trusted repository code, not a sandbox. Borrowed transaction handles, rows, and storage adapters must not escape their
callback or be used concurrently.
They must not issue transaction-control statements, modify recovery metadata,
or perform external effects. `MaintenanceTx` exposes guarded SQL methods, not the raw connection. Every
statement checks native transaction state; loss is latched across nested
scopes so catching a trigger rollback cannot lead to later autocommit writes.
The caller still owns the original connection and must not bypass the handle.
The candidate callback contract latches its first SQL or Scan failure even if
that callback catches the error. In contrast, a caught nested `MaintenanceTx.Apply`
error rolls back only that nested savepoint, unless SQLite lost the entire main
transaction. Canonical domain callbacks return SQL errors, so this distinction
does not change the verified external-cost replay cases. Parity is not claimed
for arbitrary callbacks that intentionally swallow SQL or Scan failures.
Rows are guarded too: deferred stepping checks native state, and all rows
close when the callback ends. Escaped rows cannot execute later SQL. Callers
should still consume and close rows promptly within their callback.

The external-cost package's `NewOfflineMaintenance` adapts this capability to
its domain interface without introducing a recovery-to-domain import cycle.
Its `ApplyReceipt` requires reader 10 inside the same transaction as the effect.
Other domains choose their existing source-defined reader requirement.

This is admission and transaction composition only. There is no enrollment,
automatic repair, automatic replay, global ACK, head advance, or transition to
ready. Domain replay success proves only that domain. Global reconciliation,
security checks and the remaining lifecycle still require their own migration.
