# Knowledge historical storage and privacy maintenance

This package implements the source optional storage, strict persisted edge/registry
validation, assertion/source purge, unavailable-proof pruning, and ordered
maintenance propagation. It does **not** implement knowledge admission,
requalification, applicability reads, graph queries, model exposure, or a complete
knowledge recovery port. Storage reads do not authorize exposure.

`New(main, authority)` constructs a service without creating tables. Its `Storage`
requires an exact active `recovery.WriteSession`, via `WithSession` or
`Storage.Transaction`. `PurgeInAssertionScope(store,id)` binds the exact child
provided by `assertions.Store.WithWriteSession`, including nested revisions.
Composition must return its error to the owning assertion transaction. The owner
retains durable-intent, rollback, and other privacy-effect responsibilities.

`Store.Rows(nil,nil)` is the uncapped all-scene ordered scan used for privacy
purge. Optional caller limits and the existing source entity-resolution 200-row
policy never constrain purge. Mutations raise minimumReader to 6, never append a
new knowledge intent. Schema enrollment is a fixed recovery-owned helper, called
lazily by Put only. Replace preserves admission_key and all indexed identity
columns, matching the source.

Validation uses pinned-source Zod code-point string lengths, safe integers,
strict nested object shapes, source-bound spans, and version-1 predicate/property
rules. Source schema limits are preserved. Reads and persisted writes normalize
field order through the source schema; lifecycle preserves Map/Set insertion
order, captured references, and pre-effect survivor validation.

## Verification after overlay integration

No builds or fixture generation were run while Stage22 was frozen. Generate the
actual-source oracle from repository root (using the repository's supported Node
runtime):

    node --import tsx backend/internal/knowledge/testdata/generate-source-oracle.ts

Then run from backend:

    go test -tags sqlite_fts5 ./internal/knowledge ./internal/recovery
    go test -race -tags sqlite_fts5 ./internal/knowledge ./internal/recovery
    go vet -tags sqlite_fts5 ./internal/knowledge ./internal/recovery

The oracle test deliberately fails if the generated fixture is absent. It compares
real source outputs and effect order, not a reimplementation of the source. Native
tests additionally exercise SQLite rollback, optional-table behavior, reader6
without independent intent, admission-key retention, row identity corruption,
expired child rejection, strict schema cases, exact captured fingerprints/refs,
span survival, and validation-before-effects.
