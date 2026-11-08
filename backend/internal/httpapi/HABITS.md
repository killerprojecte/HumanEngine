# Concrete habit admin routes (candidate)

## Local checkpoint acceptance

The frozen round3 candidate passed ordinary tests, race tests and vet for
habitservice, httpapi and candidateservice. Independent review checked all
15 files against that snapshot. The 26 actual Hono cases include final-readiness
rejection for schema errors and404, encoded path behavior, and enrolled deletion
failure with a durable intent but rolled-back main state. Commit-failure injection
was verified to trigger exactly once; real TCP tests cover slow response draining.
This supersedes the earlier pending-execution notes below. Full CI on the
published commit remains required. The default read-only preview and incomplete
full robot runtime are not silently promoted to production by this checkpoint.


This slice enables GET/HEAD /api/habits, PATCH /api/habits/{id} and DELETE
/api/habits/{id} through explicit NewWithHabits/OpenWithHabits constructors.
The old New/Open read-only preview is unchanged. There is no POST habit route.
The borrowed domain service must match the exact main pool and authority.

GET captures only ordered contributing habit rows and gate, releases both scopes,
then invokes the existing per-record clock/recalculation behavior. Final publication
compares those exact rows (including rowid/order) and the gate before enqueueing
owned JSON bytes. No arbitrary history/census scan or new limit is introduced.
This has two linear habit reads; it does not claim constant-time large list reads.
Raw admin records retain authorWatermarks and source fields, not List sanitization.

PATCH validates the actual strict locked schema, executes the actual owned writer,
and accepts no response before COMMIT. Its exact persisted record and gate must
still match at final publication. DELETE preserves durable habit_delete and main
ACK semantics and publishes only {ok:true} under a current matching gate. An error
following commit is an unknown HTTP outcome, never permission to replay the write.
No socket Write/WriteHeader runs inside these scopes. No callback can publish from
an unfinished outer writer; scoped publication methods reject non-root services.

The actual source createApp oracle has twenty-six route cases, including HEAD,
encoded ID, missing IDs, schema errors, malformed JSON, missing JSON content-type,
auth and unsupported POST. Only generated request IDs are normalized. Notably the
source global error handler maps malformed JSON/missing JSON media type to503,
while valid JSON rejected by the strict schema returns400. Hono's media-type regex
is preserved. Existing preview header formatting/request-ID policy is not changed.

Source generation and independent --check passed. Go compilation/tests/race/vet
are NOT RUN; this is a frozen review candidate. Native tests cover source response
and stored bytes, actual explicit constructor/shutdown/failure cleanup, commit
failure, no write replay after broken transport, callback ordinary-row edits and
withdrawals, and actual TCP drain paused while main/authority writers progress.

Remaining startup work: after full backend parity, replace cmd/humanengine's
httpapi.Open preview with the installation-owned concrete service lifecycle,
wire botruntime's full Engine/exposure gate and recall owner graph, and select
OpenWithHabits (or its fully composed successor) there. This slice does not switch
the default executable or claim the complete backend is deployed.

## Review correction round 2

Route matching preserves Hono's URI-decode/parameter-decode sequence, including
escaped slash, protected percent, encoded static text, and invalid UTF-8 groups.
The small Hono routing adaptation retains its MIT license in HONO-LICENSE.
Schema-rejected400 responses now use final authority admission and owned status/
body enqueue; a request-body withdrawal leaving an ACK gap must instead yield503.
The actual enrolled-source oracle proves DELETE failure retains one typed durable
habit_delete intent and rolls back both main row/tombstone and main ACK. Fixtures
use legal64-hex synthetic fingerprints, not the previously invalid short values.

The native COMMIT fault is now a real RegisterCommitHook with an asserted trigger
count, not an authorizer replaced by the writer. Constructor-failure tests use a
real index/table name conflict; no new runtime schema audit was added. A small
Python SQLite probe confirmed that exact DDL conflict. All Go checks remain unrun.

## Review correction round 3

Known 404 responses are accepted under one authority reservation, without a
redundant readiness preflight. Status, headers and body are queued privately;
transport draining remains outside the reservation. The actual Hono oracle now
includes ordinary GET/HEAD 404 and withdrawal during not-found materialization:
the final middleware rejects the latter with 503. The native differential injects
that withdrawal through an independent writer at the real private-header snapshot
boundary and asserts it ran exactly once, with a durable intent/main ACK gap.
No production fault hook or protective limit was added. Go validation is unrun.
