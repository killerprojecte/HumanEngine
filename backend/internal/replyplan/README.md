# Reply planning policy

This is the native Go port of `src/runtime/reply-plan.ts` and its pure evidence
and context dependencies. It implements the actual policy rather than a tool
adapter or a placeholder runtime:

- Strict source-generated plan contracts, defaults and reply-suggestion gating.
- Target/context, exact receipt/member provenance, relation closure, workspace
  state, semantic-summary refinements, delivery and action/noReply validation.
- The six-round/twelve-read source planner loop, one citation repair, immutable
  repair actions and no reads during repair. Existing source policy budgets are
  retained; no new admission limits are added.
- Exact expanded JSON byte accounting, whole-set evidence omission, coverage
  metadata and receipt delivery mutation checks.
- Per-turn context reference projection, cross-scene checks and prompt-context
  expansion, retaining non-envelope message pointers.
- Prompt cancellation when a callback-based operation ignores its signal, with
  deterministic first-settlement ownership after race attachment.

`New` requires a deployment-configured locale comparator and `*jsdate.Parser`.
Use the actual `topiccollation` native comparator initialized with the source
Node resolved locale and `jsdate.New` with its resolved timezone. Neither the
locale nor the timezone may be approximated from Go locale/tzdata defaults.
The eventual startup composition still needs native pinned-ICU default resolution
matching V8 on the same deployment environment. `nodeicu` is the natural owner
for that follow-on work. It must not launch Node to discover defaults when no
V1 plugins are enabled, or create a new manual migration prerequisite.
Native ICU data and initialization are shared by `nodeicu`. Production calls do
not invoke Node or TypeScript. The model transport, memory/exposure authority,
read-tool implementation, bot scheduler and final delivery are separate runtime
integration work; this package alone is not a migrated running bot.

## Data and callback contract

Objects/messages/evidence use ordered `*jsjson.Object`, arrays use `[]any`, and
strings use canonical WTF-8. Numbers are binary64. `nil` means JSON null;
`Undefined` represents an absent JS extension result or explicit undefined
property. JSON serialization omits undefined object fields and emits null for
undefined array entries. Negative zero and nonfinite numbers retain source
behavior, including the difference between `isDeepStrictEqual` and JSON output.
Go maps/structs, arbitrary JS prototypes, functions, symbols, cyclic values and
shared mutable array identity are not this data-boundary representation.

`ValidationInput` follows the source typed target/context/tool contract. If an
upstream evidence coverage field is present, it follows `EvidenceCoverage`:
`status` complete/limited, numeric `returnedRecords`/`omittedRecords`, and the
optional source scope/reason. The package does not add admission validation for
malformed trusted coverage metadata or claim JavaScript coercion parity for
out-of-contract coverage objects.

`Complete` and `Execute` **must initiate work promptly and return**. They receive
a settlement callback and the cancellation context. A callback that blocks
before returning cannot be interrupted by this API, just as a synchronously
blocking JS extension prevents the source event loop from progressing. A late
settlement is harmless and does not re-enter policy processing. The transport
still owns stopping the actual operation and its resources. Input objects and
projection state must not be mutated concurrently; serial callback mutation is
checked at the source authority boundary. A provider failure that settles
before a later cancellation remains that failure. Cancellation during initial
work initiation wins the initial throw-if-aborted boundary, matching source.

## Source schema fidelity

`schema/*.json` are generated directly from the actual pinned TypeScript/Zod
schemas and final tool returned by `buildReplyPlan`. Native schema traversal is
limited to those generated keywords and fails at initialization on unknown
keywords. Source refinements are ported explicitly in `workspace.go`.

Zod **4.6.7** string min/max checks count Unicode code points. Handwritten source
checks such as repair-hint ID length still count UTF-16 units. These are separate
policies; the oracle covers supplementary characters and lone surrogates.
Object stripping, strictness, record `__proto__` behavior, regexes, safe integers,
property order and default insertion are source-derived rather than assumed
from generic JSON Schema behavior.

## Verification

From the repository root, using Node v24.22.1:

```sh
node --import tsx backend/tests/generate-reply-plan-fixtures.mjs --check
```

The synthetic oracle executes the actual source validation, schemas, workspace
state/refinements, evidence budget, projection, prompt expansion and complete
planning loops. It records source hashes and exact serialized outputs/traces.
Default Go tests use an explicitly finite comparator lookup captured from Node
for those fixture keys. This is test-only, not the production comparator.
`TestActualSourceNativeOracle` reruns the same corpus with the actual pinned ICU
provider. Dedicated source-order fixtures cover provider reject/resolve and
abort in both orders, before and after Promise.race attachment.

```sh
cd backend
go test -race ./internal/replyplan ./internal/jsdate
. /absolute/path/to/pinned-icu/env.sh
go test -race -tags humanengine_icu ./internal/nodeicu ./internal/replyplan ./internal/jsdate ./internal/topiccollation
go vet -tags humanengine_icu ./internal/nodeicu ./internal/replyplan ./internal/jsdate ./internal/topiccollation
```

No model credentials, chats, databases or live memory records are fixtures.
Finite differential cases establish observed compatibility, not a mathematical
proof over all possible inputs or completion/extension implementations.

## Planner-to-replyer evidence handoff

The package also ports the complete pure handoff from `reply-brief.ts`,
`knowledge-evidence.ts`, `evidence-selector.ts`, and `reply-evidence-budget.ts`:

- `BuildReplyBrief` retains exact selected memory records, complete relation
  qualifications, state declarations, inherited AND/OR groups, historical
  windows, semantic summaries and workspace views. It filters raw proof bodies,
  internal hashes, web bodies and proposed action arguments while retaining
  source uncertainty, provenance, image caveats and actual action outcomes.
- `KnowledgeEvidence` recursively removes only proofId, backingFingerprint and
  spanHash. It does not prune semantic fields or collapse support groups.
- `SelectEvidenceGroups` implements mandatory-first ranking, UTF-16 ID ties,
  explicit selection, complete-group selection, original 40-candidate and
  12-selected-group/record policies, exact UTF-8 bytes and verified tokenizer
  counting. Mandatory failures, envelope failures, final instability and all
  omission reasons follow the actual source.
- `PackReplyEvidence` unions the transitive memory dependencies of retained
  facts, treats each bounded state receipt as one indivisible unit, honors
  original state/window bounds, preserves planner omissions and removes facts,
  suggestions and freeworded goals/strategy when their evidence is omitted.
  It keeps constraints, fixed sources, action receipts and image context.
  Diagnostic omission reasons intentionally differ from the generic reasons
  inside the winning measured payload, exactly as the source does.
- With `ContextLayout: "messages"`, usage includes every expanded user-message
  JSON wrapper. The returned serialized text is the exact winning _logical_
  projection, not a reconstructed payload or the expanded wire string.

`SelectionBudget` is distinct from the planner's accumulating `EvidenceBudget`.
`VerifiedTokenizer` is a trusted server capability. Its verified flag is not
user attestation, and these tests do not establish tokenizer calibration. Its
version/model identity and the budget configuration must remain stable during
one call. Count must yield a nonnegative safe integer, positive for nonempty
serialized input. Panics/errors fail closed without echoing callback content.

Serializer callbacks receive `ReadonlyJSON`, not writable trees. The view offers
Get/At/Keys/scalar accessors, Stringify, and ProjectedStringify. Copy explicitly
returns a caller-owned deep copy. Inputs are snapshotted once; measurements do
not repeatedly clone full candidate trees or scan them for callback mutation.
The final two measurements are still performed and compared, as in the source.
Serialization must preserve complete group meaning and be deterministic; the
source check is not a proof of determinism over every invocation. A projection
registry may grow while measuring candidates, matching `ContextProjection`.

The two selection/packing exports also return `ReadonlyJSON`. This is a Go
ownership API for the handoff, not a claim that every property of the source
result is frozen: the source freezes group/value arrays and selected brief
subtrees, but not all top-level result or usage objects. Views cannot leak a
mutable alias through ordinary exported APIs. Mutating a Copy cannot change
previously retained callback views or the returned result. Inputs and callback
state must not be mutated concurrently. `BuildReplyBrief` itself deliberately
retains the source's shallow references for plan arrays and picked nested data;
the packer snapshots that brief before invoking any callback.

The data contract above still applies: source typed string IDs and ordinary
ordered JSON data are supported, not exotic JavaScript objects, accessors,
functions, cycles or mutable shared-array identity. No new protective admission
limits or content truncation have been introduced. These functions are evidence
presentation/selection policy, not authorization or a complete robot runtime.

Additional verification, from the repository root:

```sh
node --import tsx backend/tests/generate-reply-handoff-fixtures.mjs --check
node --import tsx --test --test-concurrency=1 tests/evidence-selector.test.ts tests/reply-brief.test.ts tests/reply-evidence-budget.test.ts tests/state-evidence-budget.test.ts
```

The handoff oracle executes the current actual TypeScript modules for 1335
synthetic cases and six complete planner → brief → reply-pack traces. It reuses
synthetic workspace mutation _inputs_ from the planner corpus, never its expected
outputs. It checks exact ordered JSON and serialization/tokenization call traces,
full and failed selections, semantic refinements, transitive fact dependencies,
byte/token boundaries, upstream omissions, citation repair, source projection,
segmented wire accounting and immutable callback ownership. Default and pinned
native-ICU Go test variants run this same corpus. Dedicated tests retain callback
views across return, mutate caller copies, preserve shallow brief semantics and
exercise content larger than earlier fixed byte policies without adding a cap.
