# Reviewed manual memory commands

This package implements manual create, update, lock, reviewed public promotion,
and guarded updates on the actual assertion revision coordinator. The
MemoryService adapter supplies the real lifecycle, relation, projection,
working-set, knowledge and semantic owners. It does not proxy to TypeScript.

Update preserves source distinctions: an empty or lock-only patch preserves
origin; any explicit content field removes current supports and lineage even
when the text is unchanged. Changed text tombstones the previous fingerprint
and invalidates legacy dependents before writing. The unguarded return is the
reviewed object before support refresh; a guarded write rereads the resulting
current object. Public promotion checks eligibility in source order, purges
history before repository write, and returns sanitized public content.

Guarded commands hash the ordered JavaScript JSON payload and both expected
preconditions. Receipts contain only metadata, are checked before applying,
and record the post-checkpoint revision. A retry returns the current object
only while the receipt's version and deletion epoch still match. This includes
source behavior where invalidation does not retain a new revision. The 4096
receipt eviction policy and 160 UTF-16-unit key policy are existing source
policies, not new resource caps. Missing/null JSON preconditions are not zero.
Receipt schema setup uses a fixed typed recovery capability, never arbitrary
DDL or a borrowed raw connection. It is lazy in the command's owned write.

## Evidence and verification

`backend/tests/generate-manual-fixtures.mjs` runs real unmodified TypeScript
MemoryService/Store and SQLite. It pins Node 24.22.1, UTC, V8/ICU/Unicode,
source files, package lock and the generator itself. Its 21 scenarios contain
58 operations: 57 tested manual commands plus one explicit learned-proposal
setup trace. Additional setup operations deliberately remain fixture setup,
not claims of implementing those other facades.

`memoryservice/manual_oracle_test.go` restores each source prestate and checks
all manual command results/errors and current/source/support/tombstone/history/
receipt state against actual MemoryService owners. Native tests cover actual
projection/vector cleanup, ABORT and native ROLLBACK after revision checkpoint,
retry idempotency, metadata-only receipt eviction, expired write scopes,
transaction-loss latching, and durable eligibility head ACK.

Source generator regeneration and `--check` passed. Go formatting passed.
Go compilation, race and vet have not yet run in this private overlay; parent
integration and the coordinated heavy execution window are required before
acceptance. This package is not a claim of completed runtime/API migration.
