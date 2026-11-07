# Typed authority writes

The sealed `Revocation` interface supports all twenty authority event kinds.
`Authority.Append` and `Authority.PrepareWrite` encode only these typed intents,
validate the complete event, and hash the source-canonical JSON bytes. There is
no generic raw-event, map, or arbitrary JSON write interface.

The additional domain intents are `ExternalCostReceipt`, `ExperienceTrial`,
`SemanticMutation`, `IdentityMutation`, `WorkingSetPin`, `CommitmentHead`
(schema versions 1 and 2), and `CommitmentDelete`. Nested structs declare fields
in source Zod schema order. Required nullable fields use pointers without
`omitempty`; optional fields use pointers with `omitempty`. Identity target nil
slices mean empty domain lists. Strings must be canonical WTF-8, including lone
JavaScript surrogates when applicable.

JavaScript numeric fields use `float64`. The typed encoder rejects NaN and
infinities before JSON serialization, so they cannot become null and accidentally
pass a nullable domain validator. Domain validators enforce the source integer,
range, and transition requirements. Negative zero remains valid where the source
permits it and becomes ordinary zero only at JSON.stringify-compatible encoding.

None of the seven added kinds always appends. Identical canonical payloads
already anywhere in the authority deduplicate unless `forceAppend` is true.
Deduplication returns the current authority head, which need not be the row
containing the matching payload. Existing history is audited and retained byte
for byte; it is never normalized to the new writer's canonical field order.

`externalcost.Receipt.RecoveryEvent()` adapts the existing storage receipt to the
sealed typed intent. Its domain-specific constructor,
`ParseExternalCostReceiptIntent`, rejects unknown fields and additionally checks
the recovery revision/correction relationship. Storage permits combinations that
recovery does not; successful storage parsing alone does not authorize append.
This adapter does not write, enroll readers, acknowledge recovery, or grant
readiness. These types are building blocks for explicit business mutation plans,
not automatic replay or a newly exposed application write API.

## Source differential tests

From the repository root, with the repository's Node dependencies available:

    node --experimental-transform-types backend/internal/recovery/testdata/generate-remaining-write-events-node.mjs --check

The fixture calls the actual TypeScript contract parsers and
`RecoveryAuthority.append`. It covers 300 accepted variants, 2,364 rejected
inputs, and 21 nonfinite numeric inputs. Accepted variants include nested
structures, Unicode/lone surrogates, nullable/optional fields, negative zero,
maximum safe counters, all seven kinds, and both commitment-head versions.
It records canonical payloads and before/after/repeated/forced heads separately.
Source and dependency hashes are retained with the fixture.

From `backend`:

    go test -tags sqlite_fts5 ./internal/recovery ./internal/externalcost
    go test -race -tags sqlite_fts5 ./internal/recovery ./internal/externalcost -run 'TestRemainingTyped|TestExternalCostIntent|TestStorageReceiptRecovery'
    go vet -tags sqlite_fts5 ./internal/recovery ./internal/externalcost

Tests exercise real authority SQLite appends and reserved main-database
`PrepareWrite` transactions. The test-only corpus decoder creates typed intents
without Go's lossy surrogate decoding. Invalid shapes that cannot be represented
by the sealed types are rejected by that decoder; representable invalid domain
values are tested through both append and prepare, with no main mutation or
head change. Separate regressions preserve noncanonical historical bytes and
verify the storage/recovery receipt-contract distinction.
