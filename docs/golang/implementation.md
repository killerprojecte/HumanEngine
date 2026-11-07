# Go server migration

Status: implementation in progress on `feat/refactor-golang`. Not a production replacement yet.

The current module and integration boundaries are tracked in [coverage.md](coverage.md). The checkpoint sections below record earlier implementation stages; they are not claims that every remaining production dependency has been completed.

The baseline is GitHub master `24fe8a773d87f2f09996e259fca46524c2c02928`. The frozen OpenAPI and source hashes in `backend/compatibility` define the migration oracle. User-requested Go migration supersedes the contributor guide's TypeScript-runtime requirement on this branch. Nuxt remains the management UI.

## Modules

- `cmd/humanengine`: composition and process lifecycle, no business logic.
- `internal/contract`: existing REST validation and compatibility projections.
- `internal/credentials`: compatible AES-GCM storage and installation key custody.
- `internal/observability`: structured events, readable rendering and scoped correlation.
- `internal/storage`: SQLite main transactions and independent WAL query snapshots.
- `internal/recovery`: authority chain, eligibility, restore and publication coordination.
- `internal/httpapi`: authenticated transports; no global bot queue for queries.
- `internal/runtime`: scene scheduling, Planner/Replyer, guarded sequential delivery.
- `internal/models`: provider protocols, model routes, usage and cancellation.
- `internal/memory`: source, writes, queries, learning and derived projections.
- `internal/media`: ingestion, animation decoding and embedding/image operations.
- `internal/plugins`: lifecycle, lazy shared Node V1 and WASM V2 host.
- `plugins`: source and distributed WASM artifacts for all four builtins.

A module is not complete merely because its directory or interface exists. Production startup remains the existing server until behavior, migration, fault, load and frontend checks pass. Existing limits are compatibility facts, not permission to add new protective caps.

## Acceptance

Each vertical slice must demonstrate real work, not proxy server requests back to the old bot. Cross-language fixtures cover JSON defaults, absence/null distinctions, cryptographic bytes and role aliases. Model/adapter deterministic traces compare decisions, source dependencies, actions and receipts. SQLite fault tests cover durable intent/main rollback, old backup restore, historical corruption, cancellation and lock handoffs. TCP HTTP load tests measure latency distributions, CPU, allocations, scan counts and lock waits. Test fixtures must not send real chat messages or call paid providers.

No main merge or deployment is part of this branch's implementation. Staged commits are progress checkpoints, not an assertion of full parity.

## Implemented checkpoint: query preview

Go credential envelopes match a Node-generated Unicode/AAD fixture byte-for-byte. Installation key loading preserves existing keys, rejects missing keys when ciphertext exists and rejects symlinks/insecure modes. JSON and readable diagnostic sinks run off the request path; shutdown drains accepted events and reports delivery failures without dropping or truncating events.

`make -C backend test differential` builds a real TCP Go preview and compares settings, models and scenes against values produced by the current TypeScript Store. The preview reads an existing SQLite copy and verifies authority before publishing. Repeated unchanged reads reuse a connection-local audit; external commits re-audit. Main ACK gaps and altered chain hashes reject publication. Existing main and authority formats are not migrated or rewritten.

Run only on a test copy: `backend/bin/humanengine --read-only-preview --database /path/to/copy.sqlite`. It is deliberately **not a production server replacement**. All other routes return an explicit unavailable response. Domain event validation currently includes source, fingerprint, assertion, security-state, eligibility, source edit heads, configuration/scene flags, habit deletion, meme state/deletion and experience state/deletion; other domains reject with `recovery_event_domain_not_migrated` until ported. Full scheduling/timezone calculation parity, remaining schema custom refinements, authority writer concurrency, bot behavior and complete plugin parity remain pending. This checkpoint does not establish final performance acceptance.

## In progress: executable plugin lifecycle

A real Go/WASI reactor now runs typing-delay policy inside wazero. Its Unicode17 grapheme handling and timing have differential fixtures against Node24 Intl.Segmenter. Tests compile actual WASM bytes, invoke exports, unload both the module and compiled code, then reload. The distributed artifacts and partially migrated operations for the other three builtins are described below.

V1 now has a lazy shared Node process with separate worker isolates for enabled JS plugins. Tests verify no startup with zero V1 plugins, shared PID with two plugins, callback rejection after disable, process exit after the last disable and interrupted setup unload. Isolates are terminated, rather than pretending that deleting an ESM import reference unloads its module cache. Tool/action/hook registrations are being bridged; model protocol, adapter, scoped cache and core-send bridges still require implementation before claiming full V1 compatibility.

The plugin response schema has an additive optional `apiVersion`; legacy TypeScript plugins report V1 and the management list renders a V1/V2 badge. Generated REST types are regenerated from the source schema rather than edited manually. The Go plugin manager is not yet connected to production bot startup.

The distribution build now compiles four real WASI reactors and embeds their bytes into the Go executable (`make bundle`). `--inspect-builtins` instantiates the distributed modules, obtains their V2 manifests and unloads them. The source-free test copies only the executable to a new directory and removes Node/Go from PATH. This proves packaging and guest execution, not complete plugin behavior. Current implemented guest operations also include inert WebRead extraction, WebSearch API request/result policy and SnowLuma CQ/opaque identity handling. Their networking, caches, adapter sessions and runtime effect integration remain pending.

## Validation status

Checkpoint one passed Go race tests/vet and real TCP differential tests. Subsequent focused checks passed V1 process/isolate lifecycle, actual WASM execution for each builtin's migrated operations, Unicode/CQ/identifier/parser fixtures, 30 legacy plugin tests and runtime/frontend typechecking. The second checkpoint passed Go race tests/vet, real TCP differential tests, embedded-module and source-free bundle execution, REST contract checks and frontend/runtime typechecking. The aggregate production build was killed after Nitro bundling; a separate rerun of the complete production build passed. Existing es2019/BigInt warnings remain. These checks do not establish full backend parity or performance acceptance.

## Next compatibility checkpoint

Nonempty model HTTP fixtures now cover provider defaults, presence-only credential reporting and persisted legacy thinker routes. Provider URLs use a pinned WHATWG parser and acceptance fixtures from Node, including empty query/hash markers and non-ASCII hosts. Zod 4.6 measures its schema string limits in Unicode code points; handwritten ledger/configuration string checks still use UTF-16 units. Tests preserve that distinction rather than assuming one rule for all strings.

Plugin teardown retains a generation until it has actually exited. V1 control messages use an ordered queue independent of pipe backpressure, cancelled setup/disable can explicitly terminate an unresponsive isolate, and already-exited workers reject calls instead of hanging. WASM cancellation also covers guest free functions; failed/cancelled closed instances retire before explicit reload. No automatic replay or new production timeout caps are introduced. A crashed V1 process still requires explicit disable/re-enable; complete plugin configuration-schema validation and the remaining typed bridges are pending.

The first aggregate pass for this checkpoint passed Go race/vet, HTTP differential, bundled WASM/source-free execution and REST/frontend checks. Independent review then identified additional URL normalization, timezone acceptance, UUID edge and scene revision/coercion cases. The final corrected tree passed Go race/vet, real HTTP differential, four-module bundled/source-free execution and REST contract checks again. Coverage includes 34 URL cases, 14 model cases, 7 scene revision cases, 1,291 timezone cases and 563 recovery-event cases. Frontend/runtime typechecking passed in the preceding aggregate run; frontend files did not change afterward. The timezone acceptance census uses Node 24.22.1 / ICU 78.3 / tzdata 2026e as oracle; porting scheduling calculations is separate.

Remaining byte-level compatibility work includes lone UTF-16 surrogates accepted in some JavaScript strings: Go's standard JSON decoder replaces them. The preview is not a migration writer and must not be used to rewrite such data. The later candidate write modules now have a lossless JSON representation and fixtures, but the remaining HTTP/plugin callers still require boundary-by-boundary migration before full parity.

## Bot admission and V1 call contexts

The topic-local turn admission coordinator now executes in Go with source-pinned differential traces: 43 traces, 1,263 transitions and 32 invalid-configuration cases. It preserves quiet/addressed/max-wait decisions, exact wake ownership, generation invalidation, trusted edits, recall, one-shot WAIT rechecks and empty restart. It does not start timers or models, restore queued work, or add policy limits. See `backend/internal/turnwait/README.md` for the surrounding runtime integration contract; this package is not yet a running Go bot.

V1 calls now propagate cancellation to the individual callback rather than cancelling other calls or the entire plugin. Cancelled RPC bookkeeping retires even if plugin code ignores its signal. Tool completion expires its effect lease, while send hooks retain turn/plugin signal lifetime. Callbacks keep their original receiver, reconstructed dependency Sets and frozen scene/event data. Admission rechecks cancellation under its lock, and plugin metadata cannot replace internal registration handles or kinds. Full typed cache/send/adapter/model bridges and production manager composition remain unfinished.

Focused repeated race tests and independent review passed. The final tree passed whole-backend race tests/vet, real HTTP differential, embedded-module tests, source-free bundle execution, pinned trace regeneration checks and REST contract checks. The initial bundle link exhausted temporary filesystem space; rerunning the bundle and remaining checks with a different temporary directory passed without source changes.

## Model protocol and configuration bridge

V1 model protocol registrations now expose the legacy IDs, labels and operation metadata, keep the original method receiver, and receive a caller/plugin cancellation signal. Each request pins its registration generation so disable/re-enable cannot redirect an old request to a replacement plugin. Disabled or exited hosts do not appear in the live registry. Arbitrary plugin error text is retained only as an internal cause, not returned as a public model error or treated as retry authorization. Caller arguments are encoded before acquiring the lifecycle lock, including user-provided marshalers.

The exact existing plugin configuration validator is transpiled from the pinned TypeScript source into the distributed worker. Startup validates the plugin-normalized configuration but passes the original stored configuration to setup, preserving existing behavior rather than applying preparation/migration hooks at startup. The generator checks both source identity and Node version.

These are compatibility-host operations, not a complete model service. Provider routing, result validation, retry-safe SDK error identity, configuration persistence, typed cache/send/adapter bridges and live bot composition remain separate work. The new global scheduler likewise has real execution and differential tests, but has not yet replaced production model scheduling.

## Durable scene writes and model work

The candidate now includes the actual global model scheduler, single-segment
send/receipt state machine and coordinated SQLite write protocol. Differential
fixtures exercise the frozen TypeScript implementations. The write coordinator
reserves the main WAL writer before authority, prevents bounded connection-pool
cycles, commits durable intent before main mutation/ACK, and never retries an
uncertain commit. Preparation reads previous state under the same writer
reservation. A transaction failure permanently retires its capability, including
errors raised during row iteration, so ignored rollback errors cannot escape into
autocommit. Independent adversarial review reproduced this issue before its fix
and verified the corrected interleavings.

Scene persistence is now a real candidate service using that coordinator, with
26 Store.saveScene policy cases and five sequential SQL key-identity cases. The
lossless JSON codec preserves JavaScript code units, property order and Number
formatting. Recovery event validation and typed encoding use it, including exact
payload/hash tests for distinct lone-surrogate identities. SQL key binding is a
separate verified conversion, not a rewrite of identity/hash material.

These write services are not exposed by the production HTTP preview. Enrollment,
full recovery reconciliation, the remaining domain events, complete bot runtime
composition and plugin integrations remain unfinished. A failed durable intent
may leave an ACK gap requiring reconciliation; this is not a runnable production
replacement yet.

Settings persistence now uses the same reserved-transaction preparation. The
actual Store.set oracle has 165 supported parity cases and one explicitly
unsupported bootstrap case. It covers policy revision changes, ordered flag
intents, budget security projection, random security revision rotation and
intent-before-validation failure ordering. A missing security-state table still
requires the separate bootstrap path; the candidate reports that limitation
before writing intent. These services remain internal until the full startup,
reconciliation and HTTP composition is ready.

The V1 model completion bridge now keeps opaque continuation values in their
originating isolate. Typed private references are bound to host, registration,
protocol and model; public JSON cannot mint them. Visible result validation occurs
before claim, and cancellation before publication releases pending state. Actual
Map, Date, cyclic, function, Symbol and BigInt identities can survive reuse without
serialization. Individual worker crashes retire only their own registration;
the last worker exits the shared Node process. Independent review additionally
checks cancellation after claim ACK and late exits from replaced workers.

The assistant-message owner must explicitly release shared continuation ownership
only when its last branch/user discards it. There is no automatic finalizer or
production model-router wiring yet. The source-derived SDK retry-error bridge now preserves exact constructor
identity through explicitly configured deployment module URLs. Automatic
deployment alias discovery and the remaining custom invocation normalization
are separate composition work.

The next candidate slice adds all twenty typed authority writes and a real
maintenance capability for offline external-cost replay. Maintenance retains a
fixed verified prefix, acquires main before authority even inside deferred caller
transactions, and prevents escaped/failed SQL handles from continuing outside
their transaction. Gate parsing preserves JavaScript duplicate-key, number and
UTF-16 semantics. This still does not perform installation enrollment or global
reconciliation.

Topic reassignment, ordered routing metadata, cue parsing and admission are now
ported against the actual source. Locale-sensitive ordering has an explicit
native ICU provider built from the pinned Node archive and exact embedded data;
its tagged build and source-free provider tests are part of the feature workflow.
It does not start Node, substitute host ICU, or silently approximate collation.
The default preview build does not yet compose this provider into a live scene
actor. Full Planner/Replyer and surrounding runtime integration remain underway.

The reply planning policy now runs natively with source-generated schemas,
workspace refinements, exact context projection and complete read/repair loops.
Its source-derived date-validity parser shares one initialized pinned ICU dataset
with topic collation. Explicit locale/timezone constructors are not a deployment
migration solution: automatic native default resolution remains to be composed.

The private tool-result cache captures complete streams without a new size cap,
produces source-compatible byte-budgeted text/base64 views, and indexes active
writes and entries by plugin and source dependency for invalidation. Cancellation
can retire observation of an uncooperative iterator without waiting for its next
chunk. The actual plugin context/manager bridge still needs to own and compose
this service; passing its independent lifecycle tests does not install that bridge.

The complete pure Replyer handoff now preserves selected memory/state content,
provenance and omissions through brief construction and dependency-aware packing.
Read-only views prevent serializer callbacks from mutating retained evidence;
callers explicitly copy when they need a writable tree. Actual source tests cover
whole planning-to-brief-to-packed-prompt traces, but the real Replyer provider,
action/dispatch lifecycle and live turn ownership remain unfinished.

Prompt context windows and successful cache-prefix tickets are also implemented.
They retain original authority signatures, cold/rolling suffixes, required-target
failures and stale-completion behavior. Payloads are serialized once per candidate
rather than repeatedly serializing a shrinking array. The membership cache does
not replace the source's live raw-row authority snapshot or install model cache
hints in a production request.

## Exact runtime clock projections

`jsdate.Parser.ParseMillis` now exposes exact Date.parse milliseconds while keeping
invalid-date results distinct from valid epoch zero and parser configuration
failures. The same 82,515 actual Node comparisons verify numeric results as well
as validity, including local-time DST and Date range edges.

`intltime.Hour` ports the participation-window `Intl.DateTimeFormat` h23 hour.
The shared `timezones` package owns the existing pinned alias census for both
settings validation and formatting. UTC/numeric offsets require no ICU or Node;
named zones use the same once-verified embedded ICU data as date parsing and
collation. Its 14,378 actual Node results include aliases, historical offsets,
DST boundaries and both Date range endpoints. Default/native race tests and vet
passed, and the source-free proof now runs all four consumer test binaries.
Automatic deployment locale/timezone discovery remains separate unfinished work.

## Native conversation lifecycle

`botruntime` now executes scene ingestion, topic ownership/admission, participation,
planning/read/repair, sequential actions, Replyer grounding and guarded send receipt
handling without calling the TypeScript runtime. Source-based fixtures compare 31
complete model-request and ordered effect traces, six bot/user ingress scenarios,
recall failure ordering and tool-schema boundaries. Concurrent tests exercise
blocked models without blocking snapshots/ingress, edits and recalls, WAIT/scene
generations, unknown delivery, simulation admission and extension removal.

The scene publishes immutable body blocks and indexed message records. Received-at
parsing happens once per body; unrelated turns' receipt ledgers are not rescanned
for outcome checks. Snapshot projection and copying happen outside scene ownership.
Expiration/capacity trimming and topic history counting still have documented scan
costs; these tests do not claim universal constant-time operation.

This is native orchestration, not full production composition. Real source-event
transactions, provider/budget/accounting services, plugin capability bridges,
flow-log/SSE persistence and startup/HTTP write wiring remain required. See the
package README and coverage map. The preview executable and existing production
startup remain unchanged.

## Native source-event transactions

The candidate `sourceevents` package now stores source metadata/revisions in real
SQLite transactions, preserving exact JS identity/change/head hashes and live
Set-style subscriber ordering. `recovery.WriteSession` composes nested trusted
stores without introducing savepoints. Ordinary callback or SQL errors may be
caught by their caller; native transaction loss and cancellation expire the SQL
capability so subsequent writes cannot accidentally autocommit. Existing
`Write`/`MainMutation` semantics are unchanged.

An authority ACK does not imply that later source material or subscriber effects
completed. Actual TypeScript fault fixtures preserve these distinctions, including
durable intent/main rollback, caught failures and an independent authority append
before main commit. There is no extra final head check. Publication rejects an
unacknowledged authority gap rather than waiting for the blocked subscriber.
The TCP test exercises this store boundary, not an implemented production route.

Closed and exhausted query handles are released during a write session rather
than retained until commit. Fixed read snapshots reuse their schema inspection;
write sessions do not cache readiness across callbacks. Optional embedding quotas
and revision retention match existing source policy; there is no new default head
quota or protective cap. The 100k-history work-count test concerns warmed,
same-authority controlled writes, not arbitrary cross-connection audit elimination.

Current-snapshot restart and old-backup rejection tests do not constitute offline
restore or witness reconciliation. Runtime enrollment, snapshot qualification,
root recall composition and the source recovery adapter remain separate integration
work. This checkpoint does not enable a production Go backend or establish full
bot/provider/plugin parity.

## Runtime source authority integration

`sourceevents.RuntimeAuthority` now implements the runtime contract over real
SQLite and durable authority, rather than a permissive test guard. It preserves
enrollment flags, forged-reference stripping, ordered edits, exact body hashes,
source snapshots, display projection, raw prompt authority and persisted scene
participation. Scoped recall callbacks see the same uncommitted main transaction
without exposing SQL or installing a global active-session bypass.

The integration distinguishes an ordinary false eligibility result from a failed
source read. Explicit checked planner/dispatch ports preserve errors at their
original boundaries, including unknown delivery after adapter invocation and
authoritative receipts already returned. Only the original readiness catch turns
an exception into false. Dependency insertion order, empty-dependency shortcuts,
source-validation installation order and the source's exact cleanup error cases
are retained. Hidden ordered receipt causes remain available to diagnostics and
never enter public receipt JSON.

Actual TypeScript oracles include four ingress/edit/recall traces, twelve failure
windows, two cleanup-state cases, four callback/dependency mutation cases, an
actual durable recall/derived-validation rollback and thirty raw scene/identity
row probes. Derived validations run inside recall before ACK, with an explicit
scoped read authority; dependency and cancellation changes affect the same
eligibility evaluation. Native
tests exercise real dual-database authority, callback expiry, preparation/apply
failures, snapshot isolation, raw prompt changes and single-failure receipt cause
identity. These are functional and work-count checks, not a claim that all backend
performance costs have been eliminated.

At this checkpoint, ingress discovery and identity writes were still pending
(the next section records their implementation). Remaining work includes memory
privacy derivations joining recall,
provider/plugin/adapter composition, offline reconciliation and production API
startup. This checkpoint does not make the preview a production replacement.

## Native ingress and accepted-edit composition

The candidate runtime now registers transport metadata, discovers missing scenes
and persists OneBot identity before duplicate/recall checks. It preserves the
source ordering differences between legacy and admission-enabled callbacks,
including caught callback errors and persisted participation reads. Accepted edits
observe the trusted raw revision before normalization and re-enter routing after
edit callbacks. Replacing a callback's source-reference property does not replace
the captured reference; mutating that reference or the accepted body is checked
at the source-defined point.

Actual TypeScript fixtures cover 32 ingress cases and nine edit variants; native
tests compose the real runtime and dual SQLite authority. Lifecycle tests cover
Close during blocked routing, immutable registration publication, and a real
single-connection-pool cross-scene recall interleaving. The one-connection setup
is a regression fixture, not a new production resource cap. Routing and callbacks
perform no database wait while holding the scene message-state mutex.

These internal services do not yet enable production HTTP writes or live
provider/plugin/transport startup. Derived memory privacy stores and the other
remaining domains listed in coverage.md are still unfinished.

## Native assertion storage and nested transactions

The candidate now implements current assertion/proof persistence, versioned
history, lazy legacy import, temporal history queries, privacy body purges and
terminal deletion. Raw repository reads preserve any stored JSON root, including
null; typed consumer adapters are explicit and preserve the source's falsy-row
repair path. History return values undergo the same JSON round trip as the source,
so caller mutations cannot change inputs and nonfinite values normalize equally.

Revision coordination retains final record/proof pairs and batches explicit
eligibility transitions across checkpoints. Required retention and history-proof
providers receive the current bound Store. They cannot silently become raw-proof
or no-op production substitutes. Nested work uses owned SQLite savepoints with
expired child capabilities; caught errors restore both SQL and bookkeeping.
Durable authority intents survive main rollback, and an ACK gap remains a recovery
obligation. An after-commit callback error does not imply SQL rolled back.

Controlled writes enable the original Store's foreign-key behavior on each borrowed
connection before BEGIN. Orphan rejection and source/support/vector cascades are
covered by source-generated tests. The assertion coordinator no longer decodes
its complete retained history on every write: it reads the first valid revision
in descending order, preserving the source result even with malformed trailing
rows. Explicit history listing and provenance graph work retain their inherent
costs; this is not full performance acceptance.

Fingerprint text runs natively with pinned Node 24.22.1 / Unicode 17 mappings,
including contextual final sigma, dotted-I expansion, whitespace distinctions and
lone UTF-16 surrogates. Tests include 3,142 actual JavaScript cases and exhaustive
code-point digests. Assertion tests include 69 history transitions, 31 repository
and coordinator transitions, raw JSON roots, and four actual dual-database fault
windows. Full row snapshots are compared by SHA-256, not truncated or sampled.

The storage boundary is not the complete memory service. Derived
commitment/knowledge/semantic privacy, offline reconstruction, and same-session
runtime recall remain separate work. Native read authority and support-read reuse
are described below.
Production REST writes, provider/plugin/adapter startup and the existing deployment
entrypoint remain unchanged.

## Scoped support reads and assertion authority

Synchronous support refreshes lazily reuse the source repository's two prepared
SQL operations. They retain neither decoded rows nor permission answers. Every
read sees current transaction contents and produces a fresh JSON value; Get
keeps its source readiness check. Nested scopes own independent statements and
restore the outer scope. SQL errors evict handles without closing an executing
reentrant read prematurely. Owned session statements remain subject to the same
authorizer, savepoint suspension, cancellation and native rollback checks as
ordinary session queries. Handles expire with the callback that owns them.
This is not a concurrent shared cache or a claim of native SQLite UDF reentry
parity.

The assertion authority uses the existing Reader connection for records, history,
source events, scenes and recall evidence. It never acquires a second authority
reservation or silently substitutes an independently opened store. Privacy
provenance stays separate from current time/version applicability. Historical
references retain exact deletion epochs and retained revision scope; current
references additionally require current versions and source-event eligibility.
Positive-only traversal memoization preserves independent OR alternatives after
cycles or unavailable paths. Existing source traversal budgets are preserved,
not new admission limits.

Observation selection uses the deployment's explicit date parser and native
locale comparator. Required policy providers do not start Node or fall back to
Go lexical comparison. Constructor scope checks do not introduce an eager
business-readiness scan. Simulated, scene/person and shared-public exposure
boundaries remain distinct.

Read authority does not perform derived commitment, knowledge or semantic
privacy writes, and does not complete runtime recall. Those services still need
one bound root transaction, real capability composition and end-to-end recovery
validation. Production REST and startup remain unchanged.
