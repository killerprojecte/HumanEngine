# Scene persistence candidate

`Store.Save` implements the existing `Store.saveScene` transaction policy using
Go, rather than proxying writes to Node. `Prepare` is the pure policy portion.
It preserves omitted legacy controls, calculates identity hashes with JavaScript
JSON string semantics, ignores caller-supplied revisions, and advances the stored
read-policy revision only for the original semantic changes. Presentation-only
changes do not revoke a scene.

`Save` uses recovery.PrepareWrite. Previous scene reads happen under the same main
SQLite writer reservation as the eventual mutation. Policy revocations commit in
independent authority before the main row and ACK; those two databases are not
atomic. A failed write after durable intent requires reconciliation, not automatic
retry. The candidate recovery package does not yet implement that reconciliation.

The caller must validate the Scene schema first and own the input while Save
snapshots it. This is not an exposed production HTTP endpoint. Bootstrap/schema
migration, live bot notification, and general HTTP write error mapping are not
implemented here. SQL key binding applies Node’s UTF-8 replacement semantics for
lone surrogates, while persisted JSON and identity hashes preserve the original
JavaScript code units. This deliberately preserves the legacy collision between
different lone-surrogate IDs and U+FFFD in the SQL key without conflating their
JSON values or authority payloads.

The fixture generator executes the actual frozen TypeScript Store.saveScene
against synthetic SQLite databases, rather than copying its policy. It records
persisted JSON and authority events for 26 cases, including legacy omissions,
identity/presentation changes, revision overflow, and lone surrogates. Native
transaction tests additionally exercise concurrent revisions under real WAL. Five
sequential actual-Store key-binding cases compare stored SQL bytes, JSON values
and authority payloads across lone-surrogate collisions, an astral ID and NUL.
These checks establish this slice only, not full migration readiness.
