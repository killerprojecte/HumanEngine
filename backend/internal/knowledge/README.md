# Knowledge storage, scoped reads and privacy maintenance

This package implements the source optional storage, strict persisted edge/registry
validation, assertion/source purge, unavailable-proof pruning, and ordered
maintenance propagation. The scoped Reader implements applicability, graph query
and exact-backing discovery using real current assertion authority. Knowledge
admission, requalification, model exposure and a complete knowledge recovery port
remain unfinished. Raw storage reads do not authorize exposure.

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

## Verification

Generate the actual-source purge oracle from repository root using the supported
Node runtime:

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

The read oracle separately covers 46 reader scenarios and 30 strict query-schema
cases. Its recording authority/storage ports isolate the actual source algorithm;
real native tests cover current source edits/recall, owner binding and expiration.
Run its generator with `--check` to verify frozen bytes without rewriting them.

`ReadWorkingSetCurrent` and `ReadWorkingSetEpisode` borrow one concrete working-set
scope and actual current assertion owner. Both readers expire at callback return.
The episode entry additionally lends that same current owner to provenance/anchor
qualification, without a second publication. Knowledge and commitment settings
remain distinct. Invocation-local memoization contains no cross-read eligibility.
