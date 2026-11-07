# Assertion read authority

This module ports the source assertion authority, provenance, applicability,
proof-parent evaluation, bound-parent traversal, and domain-source-current
predicates. It is a read-authority building block, **not a completed
MemoryService, recall lifecycle, or HTTP surface**. No privacy writes or
irreversible invalidation are performed here. The production recall/edit write
integration remains separate work.

## Composition and ownership

Inside `assertions.Store.Read`, construct `assertionauthority.New(reader,
options)`. `WithSupportReadStatements` may wrap this construction to use the
same scoped prepared `Get`/`Supports` reads. `New` checks only capability
lifetime, not business readiness. The source method-level checks are preserved.

The reader supplies current records, retained revisions, revision/epoch state,
scenes, recall tombstones, and a borrowed source-events view. There is no global
session, new connection, independently entered source read, or fallback provider.
A write-bound store reads its current transaction. A publication reader uses its
existing guarded connection. All derived views expire with their owner and
reject an ancestor suspended by a child savepoint. They are synchronous and
must not be used concurrently. `WithObservation` is a pure input transformation,
like `ParentScopeAllows` and `EvaluateProofParents`; it does not borrow database
authority or check reader lifetime. The borrowed source view intentionally does not
cache schema absence because a transaction may enroll source tables later.

`Options.DateParser` must use the source deployment's resolved timezone;
`Options.Compare` must be the pinned native locale comparator for its resolved
locale. Missing policies fail construction. This package never starts Node.
The optional clock supplies the current instant, defaulting to the real clock.

## Semantic boundaries

- `Supported`: privacy provenance, including superseded retained parents.
  Version/time drift alone is not a privacy deletion instruction.
- `AnalyzeSupported`: fixed synchronous batch for lifecycle planning. Only
  positive provenance results are shared within the call, with a fresh source
  budget and empty path per root. It returns no partial result on failure and
  exposes no cache or interleaving callback. A later call rereads current SQL.
- `CurrentApplicable`: active/disputed status, validity interval, exact current
  source-event identity/revision and parent version. This never purges history.
- `HistoricalAssertion`: matching current scene/subject authorization and epoch,
  then retained exact revision, history proof filtering and recomputed sources.
- `RelationAuthority`: exact current version/epoch, privacy support and current
  applicability before returning sanitized evidence.
- Source alternatives remain OR. Required sources/parents inside a proof remain
  AND. Only successful traversal answers are memoized; cycle failures cannot
  poison a later independent alternative. Memoization is local to an operation,
  never persisted across reads or transaction mutations.
- Public parents may support a scene child. Private parents need the same scene
  and matching subject (or an absent parent subject). Simulation never exposes
  historical/relation evidence.
- Exact JSON objects preserve property order, absent versus null binding fields,
  WTF-8/lone surrogates, unknown fields, and detached exported proofs. Source
  deduplication uses the original source tuple and retains event-ref key order.
- Observation time is recomputed only from trusted receipt timestamps. Invalid
  calendar dates are discarded. Equal instants use source `localeCompare`.

The existing source traversal contracts (4096 provenance work, 512 bound-parent
work, depth 64) are preserved. They are not new protective caps. Exhaustion
returns `memory_proof_budget_exhausted`, not `false` and not a reason to erase
unknown evidence. No cap is added to applicability, proof alternatives, rows or
retained history.

## Validation

`backend/tests/generate-assertion-authority-fixtures.mjs` directly executes the
TypeScript modules with synthetic graph fixtures. It records exact JSON
results, error messages and source-port call order, plus source hashes. Generate
with pinned Node v24.22.1 and `TZ=UTC node --import tsx ...`; `--check` detects
source drift. The native-ICU Go oracle uses the source-resolved locale.

The real SQLite integration tests use actual assertions Store/Reader,
sourceevents Store/View and recovery sessions with one pool connection. They
cover same-session schema enrollment, edits, recall/scene changes, child rollback,
ancestor suspension, expiration, and current-versus-history gating after a
retained parent revision changes. Test-only comparator injection is used only
for fixtures without observation locale ties. Production requires the real
locale provider.
