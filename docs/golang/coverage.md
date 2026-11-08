# Go migration coverage and composition boundaries

This is an implementation map, not a declaration that the Go backend can replace
production. The current executable still requires `--read-only-preview` and the
existing TypeScript production startup has not been replaced.

## Latest integration status

The detailed entries below retain historical slice boundaries. See
[the exact-head acceptance record](acceptance.md) for accepted checkpoints through
native installation, provider transport and recovered-history privacy. Their
older pending-CI statements are superseded only for those verified heads.

The current integration adds [whole quarantined reconciliation](recovery-reconciliation.md),
including real offline habit/media/experience/trial owners and final verified
main ACK/readiness. It replaces the older global-reconciliation gaps below at
the package level; production boot and HTTP/runtime composition remain open.
Source regeneration, targeted private tests and twelve affected-package
integrated race/vet checks passed, followed by complete exact-head default and
native-ICU CI acceptance recorded in the acceptance document. The native image codec is now integrated as a source-only checkpoint. Its complete
exact-head CI and real standalone file trace are recorded in
[native image acceptance](native-image-validation.md); binary source/license
delivery and complete live media ownership remain separate requirements.

Live habit behavior, exact numeric decay and same-owned-session Engine proposal/
feedback adapters are integrated. The config preview remains unchanged; explicit
`NewWithHabits`/`OpenWithHabits` constructors now enable real habit GET/HEAD, PATCH
and DELETE with final publication and durable-write semantics. See
[habit HTTP boundaries](../../backend/internal/httpapi/HABITS.md). These packages
passed local full/focused race and vet checks and source oracles. The live-domain
and Engine-call head `31269fb7` also passed complete CI run42463496736, including
the frozen and current-host numeric oracles. The newer HTTP head `155515af` passed its own complete CI run42464453844,
including native ICU, actual image trace and both numeric oracles. Full runtime/exposure wiring,
learning persistence, live experience/media owners and remaining REST routes are
not implied complete by these domain checkpoints.

## Implemented candidate modules

| Module                               | Executable behavior                                                                                                                                                                        | Still outside this module                                                                                                                     |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `contract`                           | Frozen API schema defaults, refinements and lossless configuration string contracts                                                                                                        | Full API routing and remaining JS string boundaries                                                                                           |
| `credentials`                        | Canonical v1 AES-GCM, JS AAD/text decoding, key custody and transactional SecretStore                                                                                                      | Security-table bootstrap, runtime wiring and documented noncanonical reader differences                                                       |
| `jsjson`                             | Ordered plain JSON, binary64 formatting, canonical WTF-8, explicit Node UTF-8 text binding                                                                                                 | Arbitrary JS objects, Buffer decoding, transforms and caller-specific regex rules                                                             |
| `recovery`                           | All 20 typed history intents, hash-chain verification, cached append and rollback-safe intent/main ACK                                                                                     | Enrollment and full domain reconciliation                                                                                                     |
| `scenestore`                         | Actual scene write policy, identity hashes, legacy control preservation and transactional revisions                                                                                        | HTTP writes, startup and live bot notification                                                                                                |
| `settingsstore`                      | Actual settings revision/flag/security policy and failure ordering                                                                                                                         | Lazy security-table bootstrap, HTTP writes and reconciliation                                                                                 |
| `scheduler`                          | Global priority, rate, foreground reservation and learning fairness                                                                                                                        | Production model/runtime ownership                                                                                                            |
| `turnwait`                           | Topic admission, quiet/max-wait decisions, generation and wake ownership                                                                                                                   | Full topic/turn runtime and persistence                                                                                                       |
| `dispatch`                           | Guarded single-segment send and authoritative/unknown receipt arbitration                                                                                                                  | Installed transport adapters, tool-send effects and live turn ownership                                                                       |
| `modelservice`                       | Candidate routing, retry/fallback and progress-aware attempt budgets                                                                                                                       | Real provider serialization, cost/recovery authorization, accounting and assistant composition                                                |
| `plugins` V1                         | Lazy shared Node, unload, config validation, cancellation, model registration, opaque continuations and exact SDK retry-error identity                                                     | Complete tool/cache/send/adapter bridges, deployment SDK URL mapping and persistent manager                                                   |
| `plugins` V2                         | Four embedded WASM reactors and their migrated policy operations                                                                                                                           | Complete networking/cache/transport behavior and runtime capability wiring                                                                    |
| `observability`                      | Structured and readable asynchronous sink delivery, backlog/error reporting                                                                                                                | Full diagnostic catalogs, UI log query service and runtime correlation                                                                        |
| `httpapi`                            | Real authenticated GET/HEAD settings/models/scenes publication                                                                                                                             | Other routes, write composition and production startup                                                                                        |
| `externalcost`                       | Canonical receipt storage and guarded maintenance-backed per-receipt offline replay                                                                                                        | Global reconciliation orchestration and runtime accounting                                                                                    |
| `topicroute`                         | Reassignment, ordered scene metadata and admission/cue policy with exact native ICU comparator                                                                                             | Production provider composition and durable runtime effects                                                                                   |
| `assertions`                         | Native records/proofs, versioned history, revision coordination, eligibility intents and scoped prepared support reads                                                                     | Derived privacy, runtime recall, offline reconstruction and API composition                                                                   |
| `assertionlifecycle`                 | Real assertion/history/vector invalidation, ordered dependent purge and safe-history rebuild through an owned revision transaction                                                         | Full projection composition, working-set/knowledge/semantic owners, full commitment composition, full recall/delete and runtime composition   |
| `projections`                        | Metadata-only lease outbox, real FTS5 lexical/person indexes, feature gating, source eligibility and exact revision-scope adapters                                                         | Full MemoryService/recall wiring, runtime startup and large-history performance acceptance                                                    |
| `relations`, `relationstorage`       | Three-table storage, admission/review/list, proof intersection, tombstones and real same-scope assertion revision/history purge hooks                                                      | Model relation exposure, REST content/identity projection, global replay and production runtime composition                                   |
| `commitments`                        | Five-table retained storage, immutable revisions, privacy pruning/requalification, recorded-state redaction, same-child assertion refresh and admitted offline domain replay/qualification | Interactive admission/commands, global offline reconciliation/ACK, query-only prepared capture and production working-set/runtime composition |
| `assertionauthority`                 | Native provenance, current/history parent bindings, source-event qualification and same-scope readers                                                                                      | Derived privacy lifecycle, learning, complete MemoryService and runtime composition                                                           |
| `jsstring`                           | Exact Node 24.22.1 / Unicode 17 fingerprint whitespace and lowercase semantics, including lone surrogates                                                                                  | Other JavaScript string APIs not used by this boundary                                                                                        |
| `sourceevents`                       | Native SQLite revisions and real runtime enrollment, captured-source/display/prompt reads, ingress identity writes and scoped recall transactions                                          | Derived privacy domains, offline witness recovery and production composition                                                                  |
| `ingressrouting` / `profileidentity` | Missing-scene discovery and exact OneBot profile persistence before admission                                                                                                              | Live adapter startup and transport composition                                                                                                |
| `botruntime`                         | Native scene ownership, participation, full planning/Replyer/send flow, simulation, ingress/edit re-entry and concurrent cancellation                                                      | Derived memory domains, real providers/plugins and production HTTP composition                                                                |
| `replyplan`                          | Actual plan schema/refinement, repair loop, complete Replyer brief and dependency-aware evidence packing                                                                                   | Real read tools and durable authority/provider composition                                                                                    |
| `jsdate` / `intltime` / `nodeicu`    | Exact V8 Date.parse milliseconds, Intl participation hours and shared one-time ICU data initialization                                                                                     | Automatic source-compatible deployment locale/timezone resolution                                                                             |
| `toolresults`                        | Complete private streaming cache, exact read views, owner/TTL/revocation and cancellation                                                                                                  | Production V1 tool context bridge, manager lifecycle and restart ownership                                                                    |
| `promptcache`                        | Source context-window selection and successful prefix tickets, with linear payload serialization                                                                                           | Live raw-row authority service and provider prompt hints                                                                                      |

The native comparator is separately built from the pinned Node 24.22.1 ICU source
and data. Its tagged tests cover actual Node ordering across 15 locales; default
builds explicitly report it unavailable rather than substituting an approximate
ordering. Production packaging and locale selection remain composition work.

Maintenance admission keeps a fixed verified authority prefix, reserves the main
database before the authority database, and scopes guarded SQL handles to their
transaction callback. It does not mark an installation ready, enroll one, or
substitute for global recovery reconciliation.

## Composition that must still be completed

1. Establish installation ownership, schema migration and recovery reconciliation
   before declaring the candidate ready for reads or writes. A durable intent gap
   must never become a blind automatic retry.
2. `candidateservice.Open` now owns one main pool and writer authority coordinator
   shared by HTTP reads and the existing settings/scene service writes. It stops
   admission immediately and drains active calls before closing resources; borrowed `httpapi.New` dependencies
   remain caller-owned. This is an internal candidate composition, not production
   startup or HTTP mutation routing. The convenience HTTP preview still opens its
   own independent read authority connection.
3. Connect the implemented native scene/turn/Planner/Replyer state machines to
   remaining real data domains: learning, memory eligibility/provenance, history,
   outbound adapters and stream accounting. Source-event enrollment/revisions and
   ingress identity persistence are now composed through RuntimeAuthority.
   Required service interfaces and synthetic test implementations do not establish
   those production dependencies.
4. Install actual model/provider and plugin capability adapters. A continuation is
   assistant-owned shared state; a branch must not release it while another branch
   still uses the same assistant. Cost/recovery checks cannot be replaced by a
   successful test stub in production.
5. Expose remaining REST operations only after their real services and failure
   semantics exist. Keep the frontend contract and migration fixtures intact.
6. Package the complete Go composition and all builtin WASM behavior, then validate
   startup, shutdown, migration, restore, faults and real HTTP contention together.

## Performance claims and remaining costs

The tests prove specific work/ownership properties, not universal performance:

- Controlled writes on one shared authority coordinator extend an audited history
  without scanning its old rows again. The 100k-history regression checks scan
  counts across actual SQLite writes and TCP publication, not elapsed-time limits.
- A change from an independent external writer still requires a full audit once.
  Separate independently opened HTTP/writer authority connections therefore do not
  receive the shared-coordinator optimization automatically. The composed candidate
  service has actual TCP tests showing settings/scene commits without rescanning,
  ACK-gap publication rejection, cancellation and shutdown ownership.
- Support refresh scopes reuse only two compiled SQL statements, never rows or authority answers. Reads remain fresh and readiness checks retain source order. Scopes are synchronous; native SQLite UDF reentry parity is not claimed.
- Assertion coordinator flush reads the latest valid retained revision by descending
  index traversal instead of decoding all historical bodies. Malformed trailing
  rows are skipped with the same result as the source full-history projection.
  This does not remove the cost of explicit full-history requests or proof graphs.
- First-open verification remains linear in history size. Main SQL mutations,
  JSON parsing/serialization and large payloads still have their inherent costs.
- Configuration publication now accepts an immutable request-private response
  under authority and releases the reservation before network drain. Real TCP
  tests cover slow clients, concurrent writer/withdrawal and subsequent rejection.
  This does not eliminate projection construction or long writer callback costs
  under the coordinator, nor establish full runtime isolation.
- Tool-result invalidation indexes plugin and scene/dependency ownership, so an
  unrelated owner's cache does not require a whole-cache scan. Stream capture uses
  one non-prefetching iterator pump per active write, not a goroutine per chunk.
  The source expiry sweep remains linear in retained entries; an uncooperative
  extension still owns its blocked iterator/cleanup operation after cancellation.
- Non-UTC Date.parse and named-zone participation-hour formatting currently
  open/close a native ICU calendar per operation. Exact native behavior is tested, but calendar reuse remains performance
  work. Locale and timezone discovery must not start Node when V1 is disabled.
- Logging does not silently truncate/drop entries or add a queue cap. Sustained
  output slower than production can still grow the backlog. Its counters and sink
  errors remain visible. This is not a claim of bounded memory/disk use.

Passing a policy oracle, loopback test or source-free WASM test establishes only
that boundary. It does not establish full bot parity, a successful production
migration, or the absence of remaining performance problems.

### Working-set domain boundary

The implemented working-set metadata owner covers source canonical schemas,
receipt deduplication, pin intent, generation/lease jobs, mutation invalidation and
notification maintenance. Its source oracle has 27 live scenarios, 86 schema cases
and 8 storage layouts. A separate 23-scenario actual older-backup oracle covers the
quarantined offline reset/pin-replay domain. These synthetic fixtures are not
production capture/authorization ports and do not establish full restore parity.

The higher-level MemoryWorkspace/WorkspaceAuthority, final model-context identity
and runtime/HTTP composition remain unfinished. Existing policy limits are
preserved, with no new protective caps. Read-only metadata publication avoids the
main writer reservation but retains authority verification; this is not a claim
that all HTTP paths are contention-free.

### Current commitment read boundary

Current/List and declaration projection now have an actual-source 35-scenario
oracle, including source list ordering/coverage and remembered terminal-state
loss. Production composition uses the real current assertion authority on one
owned snapshot or active write child. Native tests cover source edit/recall,
expiry, quarantine, proof changes, scope mismatch, native read-only enforcement
and transaction loss. Reused prepared statements contain no cached row or
eligibility answer. Interactive command admission and complete workspace/model
exposure remain separate unfinished work.

### Fixed list and experience read projection

Actual TypeScript fixtures cover six native-SQL list scenarios with scene/person/
simulation scopes, exact-ID deduplication without prefix truncation, rowid order,
JSON/time filters, real support qualification, recall and readiness ordering.
The experience fixture includes 50 projections and 17 verification scenarios,
including duplicates, simulation validation, existing product limits, coverage,
changed evidence, exact callback references and keyed item comparisons.
These are focused domain contracts, not completion of MemoryService or workspace
capture. Native scoped tests separately exercise lifetime, suspended/foreign
capabilities, fresh pending mutations, ownership and readonly operation.

### Workspace qualification and privacy storage

The knowledge oracle executes 12 actual-source purge scenarios and 16 schema
cases, including canonical field order and Unicode/time boundaries. Its purge
ports record effects; separate native tests exercise optional schema, exact
write-child binding, rollback, readiness, retained admission keys and the absence
of invented knowledge intents. The semantic oracle executes real TypeScript
SQLite storage/lifecycle for four purge scenarios and 11 contract cases. Native
coverage includes typed durable-prefix/local-ACK gaps, attempt-write failures,
all-candidate planning, retained tombstone capacities, duplicate/hash conflicts
and exact owner binding.

The workspace oracle covers 22 actual-source resolver scenarios through recording
current-read ports. Native tests use real assertion history, registered source
metadata, edits/recall, enabled commitment protection and exact mutation anchors.
They verify ordered repeated hash inputs, same-child freshness and callback expiry.
The working-set read bridge additionally exercises a one-connection publication,
wrong-owner/ancestor rejection and native query/prepared-write authorization.
These checks do not establish complete refresh/capture, knowledge admission,
semantic episode discovery or production notification/scheduler integration.

### Authoritative refresh, capture and semantic episodes

MemoryService now composes real revision transactions, support refresh, list and
returned-body capture. Nine actual-source operations cover duplicate legacy
capture, score-only omission, reordered properties, stale bodies and simulation/
existing parent-limit ordering. Native tests additionally cover postcommit
projection failure logging, nested ownership and rollback notification suppression.
Notification delivery is deferred through a required serial dispatcher adapter and
requalifies each exact reference. Listener removal is constant-time; live mutation
and nested delivery preserve source iteration behavior without retaining closures.

Knowledge read differential coverage has 46 reader scenarios and 30 schema cases.
The source oracle uses recording storage/current ports; native owner tests use
actual SQLite and assertion/source authority. Semantic episode discovery adds 33
actual-source cases with ordered call traces. Its native graph tests use two
participants, real provenance, revision-created internal markers, proof withdrawal
and the real notification queue. Graph rows are seeded directly, so these tests do
not establish graph admission or proposal compatibility.

A working-set subloan expires its query, prepared statement and row operations
without weakening underlying native ownership/read-only enforcement. The existing
WriteSession handle remains an inherited parent capability, not a capability
revoked by that subloan. Native tests exercise both publication and write owners.

Production scheduler/source subscriptions, full proposal and graph admission,
requalification, final model-context construction and HTTP/runtime wiring remain
unfinished. These domain checks do not establish a complete bot migration.

### Trusted source admission (candidate)

The next additive slice supplies strict proposal normalization, typed declaration
preparation, trusted source evidence and subject attribution, concrete assertion
authority admission, and knowledge source spans. Its 75 actual-source cases bind
the generator and 20 source/runtime dependency hashes. Native tests use real
revision checkpoints and exact parent references, not a permissive parent hook.
The corrected recall cases distinguish surviving current OR provenance from an
unavailable historical revision whose recorded source was recalled.

This is not the complete write command: reconciliation, proposal persistence,
graph admission and historical requalification are still being composed. The
source evidence APIs accept normalized trusted internal inputs, not arbitrary
public API objects. The initial focused run found a wrong new test expectation;
the corrected three-package race and vet rerun passed. The exact published
Stage27 checkpoint subsequently passed complete CI, including the aggregate,
WASM, source-free bundle and pinned native ICU checks.

### Synchronous proposal reconciliation

The owner-bound reconciliation policy adds 95 actual TypeScript/SQLite cases
for rejection ordering, stored fingerprint tombstones, merge/correction scope,
subject witnesses and missing/null distinctions. Focused race and vet passed for
reconciliation and assertions. Native tests cover savepoint ownership/rollback,
expired publication and transaction readers, SQL failures, readiness propagation,
and early rejection before malformed stored JSON is parsed.

This decision policy performs no writes. It is not the full proposal command or
graph admission. Its parity scope is typed inputs and valid assertion/proof
records plus enumerated legacy date coercions, not arbitrary corrupt JSON or
JavaScript object ToPrimitive behavior. Complete aggregate acceptance for this
new slice is still pending.

### Inner assertion proposal writes

The inner writer composes real child transactions, admission, reconciliation,
repository writes, lifecycle refresh and sanitization. Actual TypeScript fixtures
contain nine scenarios and 47 traces. Go replays 41 proposal and three history
operations against their recorded prestate, returns/errors and SQL state. Three
external owner boundaries are explicitly setup rather than native lock/delete/
recall implementations. Sequential core traces are additionally replayed. The
fixture enforces 23 source/generator/lock hashes and pinned runtime metadata.

Focused race/vet passed for assertionproposal, memoryservice and assertions.
Concrete MemoryService tests verify SQL-failure rollback, revision bookkeeping,
FTS/vector effects and real eligibility intent/head ACK. A superseded parent can
retain a historically supported active child which is no longer currently
applicable; this is preserved. Separate explicit source-unavailability setup
exercises actual dependent purge and durable eligibility acknowledgement.

These checks do not complete the outer typed knowledge/relation/commitment
proposal command or the missing lock/delete/recall facade operations. Full
aggregate/bundle/native-ICU acceptance of the inner assertion slice passed on
remote commit abaa489e1d6fd84510e8d182c080826fcdc00376 (run 42444906014).

### Knowledge graph admission

68 actual TypeScript admission oracle cases cover planning, source-backed spans,
exposure, conflict and duplicate handling, enabled/simulation gates and advancing
clock ordering. The generator enforces its pinned runtime, UTC and source hashes.
Synthetic seeded edge IDs cannot collide with generated IDs.

Focused race tests and vet passed for knowledgeadmission, knowledge and
commitments. Native tests exercise real owner composition, source availability,
privacy, second-insert outer/nested rollback and trigger-induced transaction loss.
The loss test observes the original rollback error and sql.ErrTxDone afterward;
no staged or accidental autocommit write survives. These are functional checks,
not isolated performance benchmarks. Full aggregate and bundle/native-ICU checks
for this graph admission slice remain pending exact-commit CI.

### Commitment command writes

Normal propose/amend/confirm/complete/cancel commands now have concrete same-child
assertion and working-set composition. The command `complete` produces lifecycle
`done`. Exact exposure, first trusted evidence matching, sender fallback, typed
intent ordering and projected temporal states follow the source.

Actual-source fixtures cover 16 operations, two caught ordinary ABORT phases,
five existing-policy scenarios and 40+1 dependency batching. Native integration
reads temporal history with real historical authority and tests 41 distinct
backings without truncation. Real RAISE(ROLLBACK) tests verify subsequent command
and SQL ownership loss, no autocommit, retained intent and unchanged ACK.
Focused race/vet passed for commitments, commitmentadmission and memoryservice.
The first integration run exposed duplicate synthetic backing fingerprints;
only fixture content was corrected, preserving the production uniqueness rule.
Full aggregate/native-ICU acceptance for this command slice remains pending CI.
The outer proposal facade and robot/REST integration are still incomplete.

### Outer proposal command

36 actual-source orchestration cases inspect declaration preparation, branch
ordering and exact forwarded context; their inner writers are explicit test
ports. Separate real-owner tests cover every branch, exact and stale exposure,
original knowledge parent references, corroboration checkpoint versions, saved
backing with semantic rejection, nested JSON bodies, graph ABORT/ROLLBACK and
commitment tail intent without ACK. Four focused packages passed race/vet.

The initial integration run exposed three incorrect test expectations, corrected
against source: malformed commitment commands throw; ordinary relation replacement
does not create an authority intent; workspace checkpointing legitimately reads
commitment settings. Graph admission adds no extra such read. Production semantics
were not changed to satisfy those initial assertions. Complete aggregate/native
acceptance for this outer composition remains pending exact-commit CI.

### Manual memory commands

The actual TypeScript/SQLite oracle contains 21 scenarios and 58 operations:
57 manual operations replayed by Go plus one explicitly identified learned
proposal setup. Tests compare records, sources/supports, tombstones, assertion
history/state/parents and guarded receipts. Native tests cover real projections,
vectors, guarded replay/eviction, SQL ABORT/native ROLLBACK, eligibility intent
and ACK, expired scopes and first-use lazy schema creation.

Focused race/vet passed for memorymanual, memoryservice, assertions and recovery.
The first run exposed SQLite's ambiguous READ opcode in the unqualified receipt
prune query; main qualification fixed that exact query without broadening the
authorizer, with attached-database denial regression coverage. Full aggregate
and native ICU acceptance for this manual slice remains pending exact-commit CI.
Delete/recall privacy closure and runtime/HTTP mutation integration remain separate
uncompleted work.

## Native Meme owner checkpoint

`memeservice` supplies live owner behavior and `imagecodec` supplies real metadata,
frame PNG and thumbnail WebP operations. Full native local race checks cover the
owner, existing compression and recovery; actual-source generators are wired into
`differential` and `native-image-check`. REST/runtime media and model-read/exposure
composition remain unfinished and are not replaced with permissive callbacks.
Explicit root/task operation capabilities preserve split durable deletion and
shared analysis lifetimes without a global writer counter or new admission cap.

## Native Experience administration

Explicit candidate constructors now compose the Experience owner and real runtime
against one database and authority, alongside Habit administration. The four route
families retain authentication, body/query parsing, code-point schema limits,
allowlisted projections, conflict responses and final publication checks. Slow
request bodies and response drains do not own the authority writer.

A query witness records only the actual reads and arguments needed by the result;
it is not a full database census. Raw stored JSON presence is retained separately
from typed mechanical validation so expiry can commit before an invalid response
projection is rejected, exactly as in the source. Whole receipt null/primitive/
array values preserve recovery qualification material without becoming valid
mechanical receipts.

The 232-case actual-source route matrix, focused four-package race checks, whole
Experience owner race checks and vet passed locally. Source regeneration checks
are part of the differential CI target. Exact-commit full CI remains the acceptance
gate after publication. The command remains an explicit read-only preview until
the full production application and remaining Engine graph are composed.

## Native V1 adapter host bridge

The lazy V1 host now supports actual source-derived adapter registration and
send/history/getMessage/status behavior, synchronous reverse callbacks, precise
invocation cancellation and lease-scoped background ingress. Failed installation
owns transport cleanup before native registration, and concurrent disable has a
single termination owner. Unknown sends are not replayed. Legacy V1 readImage
omission remains unchanged.

All V1 race tests, modelservice race tests, vet and actual-source adapter/lifecycle
checks passed on the final private revision. Independent real-host reproductions
confirmed the repaired cancellation and failed-install disposal paths. Integrated
HEAD CI still supplies whole-plugin/WASM/source-free acceptance. This host bridge
is not the complete application plugin manager or runtime assembly; deployment
SDK aliases and remaining model-router work remain explicit outstanding scope.

## Meme HTTP and native runtime integration

Meme admin REST routes, native model-read policy, model/provider composition,
source-bound observation and display-image validation are connected through the
actual database owners. Publication rechecks the original full row and source
read witness in the final writer. Shared analysis retains its captured authority;
consumer cancellation does not silently acquire a fresh one.

Turn cleanup now releases opaque continuations outside the turn lock, including
late allocation and panic paths. Standalone vision analysis also releases its
owned V1 completion on success, invalid output and cancellation. Four actual
Node-host tests observe claimed handles return to zero, rather than fake counters.

The combined candidate passed the affected package race suites and vet, actual
Meme60/Experience232 HTTP cases, model-read9, display26, compression52 and large
image boundary source checks. Missing test-copy source links and an incomplete
new fixture schema were corrected and their failed runs retained. Whole integrated
HEAD CI remains required after publication. Full application startup, adapter
composition and shutdown draining are still being implemented; this slice does
not remove the read-only command gate or publish the pending media101 change.
