# Settings persistence candidate

`Store.Set(ctx, value)` implements the JSON-data boundary of the actual
`Store.set('settings', value)`. It is a Go persistence service, not a Node proxy
or an exposed production HTTP endpoint. Use `jsjson.Parse` for JSON input; a Go
map or encoding/json-decoded object does not preserve the required property order
and UTF-16 semantics. Callers must not mutate input while Set snapshots it.

The service deliberately does not run the full public SettingsSchema. The source
store preserves unknown settings and validates only BudgetSettingsSchema's
security projection. The Go projection preserves its defaults, stripping of
unknown keys, declaration order, bounds, refinements and ordered validation
messages. JSON equality is evaluated with JavaScript number, property-order and
lone-surrogate string semantics. Feedback uses strict boolean equality; knowledge
and semantic revision changes compare the original null-coalesced JSON arrays,
not boolean-normalized flags. A nonboolean knowledge flag can therefore increment
a policy revision without appending a configuration_flags intent.

Previous settings and each needed revision are read under the same main
`BEGIN IMMEDIATE` as their eventual write. The source reads a revision only if its
policy changes; an invalid but unused revision is not rejected. A settings update
writes the exact original revision increments, settings JSON and optional security
UUID, with the recovery ACK in the same main commit. Authority contains only the
configuration booleans and random v4 security UUID, never settings values, endpoint
names, credentials or hashes of them. UUIDs use crypto/rand.

## Failure boundaries and enrollment gap

Authority intent commits independently before main. If main fails, durable intent
can remain while settings, revisions, security state and ACK roll back together.
There is no retry, reconciliation, restart replay or automatic head repair here.

Failure ordering matters: the TypeScript source appends configuration_flags before
parsing budget settings. The candidate preserves that behavior. An invalid budget
combined with a changed flag can therefore leave a configuration intent, even
though validation fails and main rolls back. Invalid/overflowed policy revisions
fail before that intent.

TypeScript lazily creates recovery_security_state in advanceSecurityState, not
in initial enrollment. MainMutation correctly forbids schema changes. Until a
separate bootstrap/migration supplies the schema, security-changing Go writes
require the canonical table produced by ensureSecurityState. A missing or
noncanonical schema returns `ErrSecuritySchemaUnsupported` before **any** intent
or main mutation. This is an explicit candidate limitation, not equivalent to the
TypeScript lazy-create behavior. Non-security writes do not require that table.
The oracle includes the unsupported lazy-create case so the gap stays visible.

Only the existing canonical application schemas and ordinary data mutations are
covered. Arbitrary extra config triggers, altered constraints or other database
schema customizations can change the source's write/failure ordering and are not
a migration compatibility claim. In particular the coordinator stages all domain
intents before main row mutations, rather than interleaving revision SQL writes
with authority appends. Original invalid revision and budget-validation ordering
is explicitly tested; arbitrary revision-write triggers are not supported parity.

Inputs are jsjson data values, not arbitrary JavaScript objects with accessors,
prototypes, functions or undefined. Corrupt stored JSON is rejected by jsjson;
native JavaScript SyntaxError wording is not emulated. Transport error mapping,
full SettingsSchema validation, live bot notifications, bootstrap/migration and
HTTP composition remain separate work.

## Verification

`backend/tests/generate-settings-write-fixtures.mjs` executes the actual pinned
TypeScript Store.set against fresh synthetic SQLite databases for each case. It
does not reproduce the policy in the oracle. Source hashes, Node 24.22.1 and Zod
4.6.7 are pinned. Only random UUIDs are normalized, after checking their v4 shape;
the Go test separately checks the main security row equals the exact authority
UUID. `--check` verifies the checked-in oracle without rewriting it.

The 166 oracle cases cover 165 supported writes/failures and the explicit missing
security-schema limitation. They compare persisted JSON bytes, ordered authority
payloads, revision rows, security rotation, validation messages and ACK capability.
Additional real-WAL tests cover concurrent revision increments, repeated writes,
no-op intents, unsupported-schema rollback and refusal to silently acknowledge a
failed durable intent. These checks establish this slice, not full migration or
production readiness.
