# Live HabitService

## Round 3 acceptance

The combined candidate passed ordinary tests, race tests and vet for jsnumber,
habitservice, recoverydomains and memoryservice. Actual source regeneration
checks passed: 8,114 frozen pow cases, 53 live traces / 247 steps, seven actual
Engine recall traces, and eight offline decay cases. Frozen and same-host pow
oracles both passed; the latter did not replace the former. Default C compilation
passed and fast-math/finite-math builds were rejected as intended. Independent
review verified all 28 candidate files match the tested snapshot.

This acceptance supersedes earlier round1/round2/pending validation notes below.
It covers the concrete domain and documented recall subsequence, not the full
robot caller graph, all HTTP routes, arbitrary libc/CPU combinations, or production
deployment. Aggregate CI on the published commit remains required.


This package is the live Go implementation of `src/habits.ts`, not a port-only
facade, offline recovery adapter, or TypeScript proxy. It implements constructor
policy/schema initialization, administrative/scoped reads, trusted validation
identities, proposal, feedback, lock updates, manual deletion, source recall,
canonical scope preparation, author-qualified evidence replacement, watermark
anti-replay, decay, and privacy sanitization. It preserves the ordered JSON
records in the existing `habits` and `habit_deleted` tables.

## Composition

`New(ctx, main, authority, Options{})` requires an enrolled main/authority pair
and creates only the two source-owned tables through the fixed
`WriteSession.EnsureHabitStorageSchema` capability. Restart never enumerates
messages, relearns evidence, recalculates persisted rows, or repairs an authority
gap. Offline maintenance remains owned by `recoverydomains`.

`Transaction` owns the root SQLite writer or an owned nested savepoint.
`WithSession` binds another domain's existing writer, verifies exact main and
authority identity, and rejects expired/suspended/foreign capabilities. All
source, scene, recall and habit reads within a write use that same connection.
There is no independent connection opened within a writer, no caller SQL escape,
and no implicitly committed nested write.

Unbound reads use fixed recovery publication scopes. Source qualification uses
`sourceevents.BorrowReadScope` on that exact scope and preserves registered-event
identity, explicit revision references, legacy unedited evidence, and recall
semantics. Source errors are returned, not converted to an unavailable answer.
Each current-evidence batch is materialized before its fixed read. Injected clocks
run outside fixed publication scopes; list and identity reads recheck current
sources afterward, and identities additionally recheck the exact SQL-keyed raw
bytes. No source-authority cache escapes a read.

`RuntimeRecallPreparation` is an implemented adapter for
`sourceevents.RuntimeAuthority.WithRecallPreparation`. It invokes the actual `Recall` operation on the root's existing writer after
the root tombstone/revision revocation. Identical source payloads deduplicate
in the ledger, but each domain call still performs append validation, main
ACK and a tombstone INSERT attempt. Those calls and their failure order must
not be optimized away. The memory runtime binding preserves its own repeated
Store.recall call for the same reason. The command
must compose this preparation with the existing memory and other-domain
preparations, not overwrite them. `Recall` is also independently usable and
preserves the source's intent/tombstone-before-habits ordering. Its root recall
intent, like manual deletion intent, is durable independently of main rollback.
A failure leaves the authority gap visible and must not be blindly replayed.

Scene admission reads the persisted scene produced by `scenestore`; it checks
`learningEnabled` at every proposal/feedback boundary. There is no additional
global habit settings gate in the source, so this package does not invent one.
Settings changes from `settingsstore` participate through the same recovery
head and existing source/model-read policy. Native tests compose real settings,
scene, source, authority and habit modules rather than mocks.

## Compatibility and existing policies

- `Object` is `jsjson.Object`. Inputs use canonical WTF-8 and ordered JavaScript
  JSON objects; SQL text binding separately follows Node UTF-8 replacement.
- Fingerprints use `jsstring.FingerprintText`, including the pinned JavaScript
  whitespace/lowercase semantics. Regex matching uses ASCII-insensitive matching
  to preserve the source's non-Unicode `/i` behavior.
- Shared expression outputs remove source identities and author watermarks.
  Administrative reads retain evidence; current-use qualification never erases
  historical stored support. Simulation writes reject, and simulation reads
  include only genuinely shared expression hints.
- `concise`/`detail` map to one signed axis. Technical verbosity is scene-local;
  ambiguous personal, quoted and image-specific claims cannot become global.
- Same-source replay cannot change direction, age or support. An authenticated
  adapter/account/sender author contributes only the latest vote. Watermarks
  survive recall, and compaction cutoff prevents discarded support replay.
- Existing source policies remain unchanged: 1–32 cited sources per domain
  write, 1000 UTF-16 content units, default 30-day half-life/90-day retention/128
  retained authors, configurable source ranges 1–365/half-life–730/8–512. These
  are pre-existing product semantics, not newly introduced protective caps.

Methods snapshot caller JSON before mutation and return owned record values.
The caller must not concurrently mutate input during materialization. An injected
`Now` receives the service bound to its current writer; reentrant writes must use
that service instead of reopening an unbound writer. Ordinary read clocks receive
an unbound service and may modify state before the final evidence/raw recheck.

## Remaining production caller wiring

The domain is implemented; these callers are **not** silently claimed migrated:

1. `src/admin.ts` habits GET/PATCH/DELETE handlers and corresponding command
   composition still need the Go service wired behind their generated contracts
   and response publication boundary.
2. `Engine.proposeHabit` and the habits feedback tool need `HabitProposalSchema`
   / tool argument validation, trusted runtime message-to-source/evidence mapping,
   Date.parse policy, source-snapshot availability guards and simulation tool
   error mapping. They can call the implemented `Propose`/`Feedback` operations;
   no callback in this package substitutes for that missing dispatch.
3. `Engine.learnedHints` needs the existing model-read ticket, first-original
   source dependencies, capture-before-exposure identities and turn validation
   callbacks wired to `AdminList`, `List` and `ValidationIdentities`.
4. The habit-first `memory/learning/persistence.ts` batch and its partial-commit
   non-replayable progress accounting remain a separate dispatcher composition.
5. Engine meme-analysis expression preferences (`src/engine.ts` around line 1673)
   must obtain scoped `List` hints before filtering `botExpression` records.
6. The command must preserve the full recall order: experience.revokeSource,
   then memory, then memes, then habits, then learning queues. It must combine
   the existing memory BindRecallAuthority preparation with all other owners,
   not overwrite it. This package supplies only the concrete same-root habit
   preparation; the two-domain source/habit test is not a completed engine recall
   binding and does not register a global callback at startup.

Malformed non-record stored JSON is rejected with package errors. Native JS
SyntaxError/TypeError wording for corrupt out-of-contract rows, object accessors,
prototypes and arbitrary JavaScript callback exceptions is not emulated. There
is no claim of full backend migration or production readiness.

## Evidence and validation status

`tests/generate-habit-service-fixtures.mjs` invokes the actual pinned TypeScript
HabitService and Store with synthetic inputs. `testdata/source.json` freezes 53
traces / 247 steps, raw persisted JSON bytes/order, fingerprints, identity hashes,
source edit and clock-callback rechecks, and fractional-day persisted decay with
default/custom 7.25-day half-life. Round 2 confirmed that Go math.Pow differs
from actual JavaScript exponentiation in persisted bytes; the shared native
numeric correction below awaits round 3 exact replay. Only newly generated random habit IDs
are renamed to deterministic IDs in the source SQLite database before later
identity calculation. The oracle is not a copied policy.

Generation and an independent `--check` passed under Node 24.22.1. Go code is
formatted. The first parent-coordinated run compiled but failed fixture setup:
its main database directory contained its authority directory. That fixture
was corrected to independent sibling `live` and `authority` directories with
an upfront AuthorityPath check. Round 2 then compiled and executed the domain
operations. Only the four fractional-decay cases failed exact persisted JSON;
the original 49 scenarios and native integration tests produced no failures.
The round 2 suite therefore failed, and the numeric correction now awaits
round 3. Race/vet and affected aggregate checks for the assembled changes
remain unrun.

The next coordinated focused command from the assembled backend is:

    CGO_ENABLED=1 GOMAXPROCS=1 go test -tags sqlite_fts5 -p=1 ./internal/jsnumber ./internal/habitservice ./internal/recoverydomains ./internal/memoryservice

Then run the affected packages under `-race`, focused recovery/source-event
tests, `go vet -tags sqlite_fts5`, and affected aggregate checks before
publication. Native tests exercised in round 2 include a one-connection
deadlock probe (test-only), concurrent author writes, real WAL persistence,
owned savepoints and rollback, source edit qualification, durable intent/main
ACK failure, native transaction loss, foreign/expired capabilities,
settings/scene/runtime habit composition, physical DB reopen without replay,
and alias/privacy boundaries. The subsequent enrolled recall composition and
numeric/offline additions still require their first Go execution.

## Enrolled runtime recall evidence

`tests/generate-habit-recall-composition-fixtures.mjs` executes the actual
`Engine.runtime.recall` on a disk-enrolled Store with its independent authority,
without starting adapters/models. Seven frozen traces instrument real append
attempts, main ACK statements, and tombstone attempts. Generation and `--check`
passed. Successful source recall orders root, experience, memory, meme, habit,
then the two learning queues. Root/memory/meme/habit each invoke Store.recall:
four append attempts, four ACKs and four tombstone attempts, but **one** durable
source event because the source ledger deduplicates identical payloads.

Injected memory/meme/habit append or ACK failures retain the root durable event
while rolling back all main ACK/tombstone/habit state. The habit tombstone failure
also occurs even when the root already inserted that tombstone, since the source
executes INSERT OR IGNORE again. This corrects the earlier retraction-only helper
and memory runtime rootApplied shortcut without changing ledger dedup policy.

The new native memoryservice composition test retains `BindRecallAuthority`
and its post-commit completion and supplies habit preparation through the
runtime's subsequent same-root callback. It compares the root/memory/habit
ACK/tombstone subsequence to the actual Engine trace. It deliberately does not
claim that absent production meme/experience/learning adapters have been wired.
The new native composition test and recall-specific corrections have not yet
been executed; they await the parent's coordinated round 3 validation window.


## Exact numeric correction after round 2

The parent-run round 2 compiled and reached real operations. The original 49
source scenarios and native integration tests produced no failures. All four
new fractional-day/default-or-custom-half-life scenarios failed exact persisted
JSON comparison with Go `math.Pow`; for example default 30 days produced
`-0.00162377611653316` instead of source `-0.0016237761165331544`. This is a
confirmed persisted-value/identity defect, not response-format noise.

Both this package and offline `recoverydomains` now call shared `jsnumber.Pow`.
It is a minimal CGO bridge implementing the actual Node 24.22.1 V8 default
`use_std_math_pow` wrapper and native `libm` call. No Node runtime is launched.
The old V8 fdlibm implementation is not the active source path on the oracle
host. Native Go tests for this correction are pending the coordinated build
window; Node generation/check passed for 8,114 bit-level pow cases and eight
actual HabitService recall traces specifically exercising offline persisted
bytes/identity. No numeric comparison has been loosened or rounded.

See `../jsnumber/README.md` for platform provenance, BSD attribution, and the
same-host versus frozen cross-host verification commands. A Node version alone
does not pin libc or CPU math dispatch. The production image must match the
source deployment's numeric ABI, or cross-host strict verification must expose
and resolve differences. Do not silently use Go `math.Pow` on non-CGO builds.
