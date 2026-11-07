# Native runtime source authority

`NewRuntimeAuthority(store)` implements the real `botruntime.Authority` over the
source store's owned SQLite installation and independent recovery ledger. It does
not open a replacement database, enroll installations, reconcile old backups,
start a bot, or enable production routes. The caller retains resource ownership.

## Source identity and observation

The bridge uses the exact `jsjson` ordered-object/WTF-8 representation for source
identity, body hashes, captured references, media qualification and prompt rows.
Only the SQL text binder converts unmatched UTF-16 surrogates to UTF-8 replacement
characters. Messages and tools are deeply copied before a read or write scope.
Bodies/media locators are never added to recovery/source metadata.

Ordinary ingress strips caller-supplied source references when revisions are off.
Existing enrollment remains authoritative with the flag off. Enabled ingress
accepts only `new` or the original optional embedding `capacity` result and
preserves the trusted body and registration-limited flag. Edits require an
available live user, enrollment, exact identity and the source's explicit ordered
sequence/CAS protocol. A repeated body cannot revive an earlier captured revision.
No new limits or quotas are introduced.

## Fixed reads and errors

Each ready/source/dependency/display/prompt/participation operation performs a
fresh authority check. Normal reads execute the fixed internal SQL under the
existing `Publish` snapshot; external callbacks never receive a SQL handle or a
reusable authority token. The prompt key includes exact raw settings/models/rules
and scene rows, including missing rows and insignificant JSON whitespace. Live
participation reads its persisted scene even with prompt windows disabled.

The runtime installs the captured-source validation at the original preparation
point, before extension validations. Its final dependency check has the original
empty-input shortcut and preserves dependency insertion order. Source/database
errors are returned, not converted into stale/recalled false values. Only the
source's explicit `eligible` readiness catch returns false. Checked dispatch and
planner ports propagate errors at the original effect boundary; a known sent or
unknown receipt remains recorded even if a subsequent source read fails.

## Recall callback transaction

Recall opens one `WriteSession` and invokes preparation before appending durable
root intent. It writes the main root tombstone, revokes enrolled source metadata,
applies native runtime changes, and runs prepared extension closures in that
order. A failed preparation still records root intent before main rolls back.
Main rollback never removes durable intent, and an ACK is not proof that every
business side effect completed. Native transaction loss expires SQL rather than
allowing accidental autocommit.

The new `PrepareRecallScoped`/`RecalledScoped` hooks receive a narrow
`ReadAuthority`. It reads the same transaction, checking readiness on every fixed
read. It is active only during preparation and prepared callback execution, never
between those callbacks or after return. No authority lock crosses a callback.
The callbacks are synchronous and must not retain/use the facade asynchronously.

Legacy extension hooks remain supported and keep extension insertion order. A
legacy closure that explicitly captured the ordinary unbound authority is **not**
automatically rebound: after root intent it sees the independently committed ACK
gap. Built-in domains must explicitly receive scoped services; no global or
thread-local active-session bypass is installed.

## Verified slice and remaining composition

The real-source oracle runs TypeScript `Runtime`, `Store`, source helpers, fixed
read projections and prompt authority, using synthetic input. Native tests run
those traces against real WAL main SQLite, a separate DELETE-journal authority,
and the real Go botruntime. Additional tests cover callback lifetime, durable
preparation/apply failures, corrupted source reads, source-body mutation isolation,
raw prompt invalidation, and failures before/after physical receipt boundaries.
The oracle generators are `generate-runtime-source-authority-fixtures.mjs` and
`generate-runtime-authority-fault-fixtures.mjs` in `backend/tests`.

This is a source-authority vertical slice, not complete bot/backend migration:

- `RouteIngress` now composes missing-scene persistence and OneBot identity
  observation before duplicate/recall checks, including bot messages. Transport
  registration survives routing failure without allocating a message-state owner.
  Accepted edits re-enter this route after callbacks and validate the captured
  source reference/body at the same point as TypeScript. This is internal runtime
  composition; live transport and production startup remain separate work.
- Memory assertion, commitment and semantic privacy preparation/redaction stores
  are not implemented by this bridge. Their typed domain stores must eventually
  join the same write session. A read facade is not a substitute for those writes.
- Full V1/V2 plugin wiring, live provider/adapter composition and production
  startup remain separate migration work. There is no TypeScript fallback proxy.
- The native `TurnResult.CleanupErr` carries an unexpected cleanup failure that
  the source exposes as a rejected `run` promise, separately from its final turn
  status and already recorded physical receipts.

Derived turn validation is part of the recall transaction, after prepared
extension callbacks and before ACK commit. `TurnContext.AddScopedValidation`
receives the same explicitly scoped read facade, so source reads can observe the
uncommitted tombstone without recursively entering another authority transaction.
A validation error rolls main state back while leaving the durable root intent
unacknowledged. Legacy `AddValidation` remains for pure callbacks; capturing an
unbound authority in such a callback does not implicitly rebind its connection.
Eligibility evaluates validation callbacks before copying dependencies, then
rechecks cancellation and the current local recall view. Callbacks adding a
recalled dependency or aborting cannot authorize the current operation.
