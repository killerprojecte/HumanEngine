# SecretStore persistence candidate

`NewStore`, `Has`, `Get`, `Apply`, and `RedactionValues` implement the source's
server-only credential store. This is a Go domain service, not a production HTTP
mutation route or an enrollment/reconciliation service. Mutation.Value uses
jsjson's canonical WTF-8 strings. Omitted/null mutations use a nil pointer.
Only JSON-data mutation shapes are covered; arbitrary JS accessor/prototype
behavior is outside the boundary.

The source policy rejects only non-string/blank set values and strings exceeding
16384 UTF-16 code units. The exact JavaScript trim whitespace set is used, not
Go Unicode whitespace. Values are not trimmed before encryption. Preserve and
missing clear do not load/create keys or rotate security. Every successful set,
including setting the same value, rotates security with a fresh random v4 UUID.
Clear does not need an encryption key.

SQLite bindings use Node's UTF-8 replacement semantics while AAD uses the original
caller UTF-16 identity. Two different lone surrogates can address the same stored
row but cannot authenticate each other's ciphertext. RedactionValues reads stored
replacement names and therefore can fail authentication for such source rows.
No lossy normalization is applied to AAD. Cipher reader limitations documented in
COMPATIBILITY.md still apply; this store does not broaden the existing cipher.

Readiness is fenced using Authority.Publish. Apply checks readiness before
validation or key operations, encrypts before entering the write transaction,
and uses Authority.PrepareWrite for the security UUID and ciphertext/delete.
Authority stores only kind and a random UUID, never credential identifiers,
plaintext, ciphertext, or low-entropy hashes. Main security revision, ciphertext,
and recovery ACK commit together. SQL errors roll them back but may leave the
already committed authority intent. Subsequent reads/writes refuse that gap;
there is no implicit retry, ACK repair or restart replay.

The key is lazily loaded/cached with existing LoadKey custody APIs. Its first-load
missing-key decision is made under the same fenced current secrets observation,
not a stale observation from initial validation. No missing key is replaced while
ciphertext exists. Key creation/encryption can precede a later failed transaction,
as in the source. Plaintext results must only be used for server credentials or
diagnostic scrubbing, never serialized or logged. The service itself has no logs.
Canonical source SQL errors are propagated without wrapping input values; arbitrary
custom database triggers that manufacture sensitive error strings are not a
supported logging policy or source compatibility claim.

## Bootstrap and concurrency boundaries

The source constructor creates secrets even before domain-ready operations.
NewStore likewise performs that exact DDL explicitly at initialization, outside
restricted mutation. It requires the caller to supply an already bound authority;
it does not enroll a new installation. The source lazily creates security-state
inside advanceSecurityState. The candidate instead requires its canonical schema
before a security-changing write, since MainMutation correctly denies DDL.
A missing/noncanonical security table returns secret_security_schema_unsupported
before intent or main mutations. Encryption/key loading still precede that error.
This bootstrap gap is tested and is not claimed equivalent to TS lazy creation.

One installation should reuse one Store and authority coordinator. Admission is
context-aware and serializes key caching and operations. Multiple Stores sharing
the same coordinator/key path are also exercised. Fenced reads prevent publication
across authority changes. Clear checks existence before its transaction, then
rechecks inside the reserved write snapshot; a concurrent already-completed delete
therefore does not add a redundant security intent. This is a deliberate concurrent
strengthening over a source check followed by a separately started transaction.
Redaction first captures names and then performs fenced Get calls like source;
it is not a single immutable aggregate snapshot under external concurrent writes.
Offline-maintenance and unenrolled in-memory runtime bypasses are not implemented.

## Evidence

`backend/tests/generate-secret-store-fixtures.mjs` runs the actual pinned TS class
against synthetic in-memory SQLite, with an ephemeral random key entirely in
memory. It uses no real key file, user DB or environment settings. `--check`
verifies 52 checked-in cases. The oracle covers lifecycle, repeated sets, mutation
validation, all trim whitespace, UTF-16 bounds, surrogate key collisions,
redaction behavior, and insert/update/delete rollback. The missing-security-schema
case records the explicit bootstrap difference.

Go tests run those fixtures on temporary enrolled WAL databases and independent
synthetic authority ledgers. Tests additionally check exact durable UUID/main
revision equality, intent/ACK rollback gaps, fail-closed reads after a failed
mutation, key-loss ordering, concurrent shared-coordinator writes, readiness before
invalid mutation errors, canceled queued admission, and malformed security schema.
TS's in-memory authority bypass cannot model durable intent gaps; those cases are
checked separately rather than pretending the source in-memory reads represent an
enrolled installation after failure. Focused race tests and vet cover this slice,
not full runtime or migration readiness.
