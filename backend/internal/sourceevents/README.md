# Native source event metadata store

This candidate ports `src/memory/storage/event-revisions.ts` using native SQLite
and exact JS JSON/WTF-8 identity/change/head bytes. It stores metadata only, never
chat bodies or media locators. The executable remains read-only-preview; this is
not complete runtime integration or a production migration.

`New` performs no SQL, initialization or reader upgrade. `Observe` and
`RevokeAll` own a recovery write session unless explicitly bound with
`WithSession`. Binding checks the exact database and authority once. Nested
operations join that transaction, without hidden savepoints. Callbacks receive
the bound store so their synchronous nested work cannot deadlock by reopening a
writer. `Read` uses the existing authority publication boundary, never a bot
queue. Its schema presence result is reused only within that fixed read snapshot;
a bound write session does not cache readiness or schema across callbacks/DDL.

Revision retention defaults to 32 and edit-key retention to 128. The optional
embedding settings retain the original source ranges (1–256 revisions, 1–1024
edit keys, 1–1,000,000 optional scene/total head capacities). These are existing
TypeScript policies, not new protective limits. Normal application defaults
have **no head quota**. Recall tombstones are monotonic and never evicted.

`Listener` pointers provide explicit identity matching JS Set identity. Iteration
is live: deleting a pending listener skips it; newly added listeners run during
the same notification; deletion/readdition moves a listener to the live tail.
Listeners run without the registry/session mutex or an authority reservation.
The main SQLite write transaction remains open. Each receives a separate
mutation copy. The fixed post-write notification is still inside the original
transaction, and recall intentionally has no committed notification.

Actual-source synthetic fixtures are generated with pinned Node 24.22.1:

- `node --import tsx backend/tests/generate-source-event-fixtures.mjs --check`
- `node --import tsx backend/tests/generate-source-event-recovery-fixtures.mjs --check`

See recovery/SESSION.md for durable intent/ACK gaps, callback failure behavior,
and the intentional prevention of accidental autocommit after native rollback.
ACK alone does not prove the source witness or all subscriber effects completed.

Options.AssertReady is a local-only readiness hook, not a second recovery hook.
It must not call Authority methods: a Read view is already inside Publish's
fixed authority scope. The default performs native recovery checking itself;
callers should normally leave this option unset. The optional capacity counts
use count(event_id), equivalent to count(*) for the source schema's NOT NULL
column, without broadening SQLite's blank-database authorizer permission.

The backup tests reopen real SQLite snapshots and reject an old recovery gate;
they do not implement global restore reconciliation or source-witness replay.
The HTTP concurrency test is a dedicated test endpoint over Publish, not the
production HTTP router. The 100k-history work-count test covers controlled writes
through one warmed Authority; independent writers retain the existing full-audit
boundary rather than inheriting that connection's cache.
