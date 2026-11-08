# Synchronous reconciliation

Ports the actual `src/memory/write/reconciliation-policy.ts` decision policy.
`New(reader, assertionauthority.Options)` accepts a concrete active assertions
Reader, not independent callbacks. `Inspect` takes the prepared context,
candidate, scene scope, accepted attribution, and ordered parent IDs.

All SQL uses the same publication/write child. No writes, new publication,
limits, or durable permission are introduced. The result retains the actual
stored row (including its fingerprint) and optional superseded record. The
caller must compose preparation/reconciliation/writes in the appropriate owner;
this package does not implement the outer ProposalCommand or graph admission.

Important parity boundaries:

- Missing and explicit null fields remain distinct for undefined checks and
  optional strict equality. Falsy supersedes IDs skip correction.
- Mentioned-witness protection precedes existing proof compatibility, then
  merge checks, correction checks, and parent-dependency rejection.
- Empty proof sources satisfy every; invalidated/recalled proofs are removed.
- Source-only exact merge compares content fingerprints, but the target's
  deletion test uses its stored row fingerprint, including historical values.
- Date.parse uses the supplied deployment parser. Invalid dates remain NaN-like
  (not expired). Date.now uses the supplied clock or real wall-clock milliseconds.
- Correction ownership compares previous scope person against candidate person,
  not normalized scope person. Learned active public knowledge requires nonempty
  sources all owned by the current scene.
- Operational SQL, parser, and expired-owner failures remain errors.

## Validation

The generator imports the actual TypeScript policy and AssertionRepository and
uses real in-memory SQLite. It pins Node v24.22.1, UTC, policy/repository/helper
hashes, lockfile, and generator. The standalone Go oracle evaluates all 95 cases
through concrete recovery write children and native SQLite, without Node.
Owner tests cover absent owner/parser, ancestor use during a child, rollback,
same-child read-your-writes, transaction/publication expiry, and SQL failure.

From repository root:

    TZ=UTC node --experimental-transform-types backend/tests/generate-reconciliation-fixtures.mjs --check
    cd backend
    go test -tags sqlite_fts5 ./internal/reconciliation ./internal/assertions

Use `RECONCILIATION_ROOT` and `RECONCILIATION_OUTPUT` only for private overlay
source generation. Go tests are intentionally not executed by this overlay's
worker until the parent grants the shared compilation window.

The parity contract is typed policy inputs and valid assertion/proof records,
plus the enumerated legacy date coercions. This is not a claim of arbitrary
corrupt JSON or arbitrary JavaScript object ToPrimitive equivalence. Malformed
stored JSON must still remain unread when an earlier source rejection applies.
