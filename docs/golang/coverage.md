# Go migration coverage and composition boundaries

This is an implementation map, not a declaration that the Go backend can replace
production. The current executable still requires `--read-only-preview` and the
existing TypeScript production startup has not been replaced.

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
- Publication currently holds the authority reservation through response emission,
  preserving the existing contract. Slow-client contention and full runtime
  composition still need explicit evaluation; this has not been declared solved.
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
