# Go migration remaining work and handoff

Documentation reviewed at Go head `5b7f012d`; implementation/CI checkpoint:
`58e48dc6d3727abc68c3bcc4b1917b54f69acbb9` (2026-11-11).
The frozen compatibility oracle remains master
`24fe8a773d87f2f09996e259fca46524c2c02928`, as recorded in
`backend/compatibility/baseline.json`. The newer legacy behavior references below
are intentional deltas, not a silent replacement of that frozen oracle.
Handoff updated: 2026-11-17. This document is a work plan, not a completion claim.

[Exact-baseline CI 43051645297](https://github.com/killerprojecte/HumanEngine/actions/runs/43051645297)
passed the compatibility job: default race/vet, actual-source differential checks,
WASM/bundle, SQLite/source-free proofs, native ICU, native image and numeric
compatibility. This establishes those tested boundaries, not whole-product parity.
No application tests or builds were rerun for this documentation-only handoff.

## Current executable truth

`backend/cmd/humanengine/main.go` exposes an explicit **writable
`--runtime-preview`**, implemented in `runtime_preview.go`. It opens the actual
`internal/app` owners and has native subprocess startup, held-request shutdown,
restart and duplicate-writer tests. It is not merely a collection of packages.
`--read-only-preview` remains read-only; the normal command still refuses to
replace production. See [runtime preview](../../backend/docs/runtime-preview.md).

Docker, package startup and the session/WebSocket gateway remain on the existing
production path. Unsupported API operations still return 503; HTTP 101 parity is
not established. Existing coverage/SCOPE entries describe historical checkpoints:
claims such as “no launcher” or “only read-only” are superseded by the code above,
not evidence that every other historical gap remains open or has been closed.

The native application already assembles real installation/recovery, providers,
Planner/Replyer/send, V1 host, memory/knowledge/relation/commitment owners, learning,
Meme/Experience services and multiple admin routes. Exact endpoint and platform
coverage must be enumerated against OpenAPI and real owner tests before claiming
full compatibility. Do not infer coverage from a directory, constructor or green
package suite.

## Delivery order

1. **P0: publish a reproducible packaged preview vertical slice** below, without
   changing production default startup.
2. **P1: land the three updated runtime behavior contracts** below in TS and Go,
   with explicit changed-behavior fixtures rather than treating old bugs as parity.
   These can proceed alongside P0, but must precede production cutover.
3. **P2: close API/platform/plugin and live-owner coverage gaps** using a named
   compatibility matrix and real-boundary tests.
4. **P3: whole-installation migration, restore, load and cutover acceptance** on
   one exact integrated commit. Package CI alone cannot authorize this step.

Every implementation PR should name the specific boundary it closes, its source
or new-product contract, commands actually run, remaining omissions and exact SHA.
Do not replace missing behavior with permissive callbacks, invented protective
caps, silent truncation or an automatic replay of uncertain effects.

## P0: next executable vertical slice

**Outcome:** a clean machine can build and launch an opt-in native runtime-preview
image, write and reread one authenticated settings/model configuration operation,
stop during an accepted slow HTTP request, restart the same disposable
installation, and reject a second writer. The existing production image/default
must remain unchanged in this slice. It is not an all-API or platform release.

Concrete work:

- Extend packaging alongside `Dockerfile`/`compose.yaml`, not by silently replacing
  their entrypoint. Package the command, pinned ICU/data, libvips distribution and
  licenses, sqlite-vec, all builtin WASM artifacts, and the V1 worker/Node runtime
  layout required by `backend/plugins` and `internal/plugins`.
- Use `backend/cmd/humanengine/runtime_preview.go` preflight and `app.Open` as the
  actual entry boundary. Define explicit database/authority/plugin mounts,
  locale/timezone inputs and admin-token environment input. Do not embed secrets.
- Reuse `runtime_preview_native_test.go` and the application/installation writer
  lease tests to drive the packaged binary, not a health stub. Verify HTTP auth,
  a real persisted write/read, SIGTERM while a request body is held, physical
  owner drain, restart/reopen and different-port duplicate-writer rejection.
- Prove zero enabled V1 plugins starts no Node process; prove an enabled fixture
  plugin loads from the packaged layout. Record missing optional platform behavior
  explicitly. No real chat messages or paid provider calls in acceptance fixtures.

Acceptance artifact: reproducible build recipe, dependency/license inventory,
source-free packaged trace, exact image/commit identity, commands and no-skip test
results. CI prerequisites below are necessary but do not substitute for this new
packaged trace. Resolve packaging defects before widening the slice to live
platform ingress or switching production startup.

## P1: updated runtime semantics, not yet delivered by this baseline

The user requested these changes on 2026-11-17. Legacy behavior commit
[04b1817](https://github.com/killerprojecte/HumanEngine/commit/04b1817af8055d58021c85e94d1f72be914125d6)
is now on `master`; the tested reference is its successor
[b7f3f92](https://github.com/killerprojecte/HumanEngine/commit/b7f3f92b0d27168955f507535e38c00ed15c6826),
which makes the typing-cancellation test explicit. Branch publication is not
proof of deployment or a Go port. Use these behavior fixtures to define deliberate
deltas from the frozen `24fe8a77` compatibility baseline.

### P1-A: learning advice on demand, learning work off the reply path

Two separate paths must not be conflated:

- **Ordinary learned scene/expression hints:** legacy `src/engine.ts` now exposes
  `rules.list` on demand instead of preloading accumulated advice into initial
  context. Its optional `query`/`context` filters return manual and learned rules.
  `src/runtime/planner-evidence-budget.ts` withholds oversized rule receipts as
  a whole under the existing byte budget; it does not silently count-truncate
  the library. Dependencies attach when the measured receipt is delivered.
- **Optional experimental Experience policies:** Go
  `app/experience_planner.go::experiencePlanner` calls
  `experienceservice/runtime_hints.go::PlannerContext`, then
  `botruntime/planning.go` includes its result in `policyContext`. This path
  selects/deduplicates at most three policies. The legacy reference retains this
  separate experimental path; it is not the ordinary learned-hints fix and must
  not be presented as evidence that ordinary advice was already bounded to three.

Port the ordinary on-demand contract into the Go application/tool/read budget
chain, auditing `botruntime/prepare.go` extension `learned` evidence separately.
Keep proposal/consolidation work in the existing background learning owners:
a read tool and a background learning job are distinct operations. Preserve
source eligibility, scene/privacy scope and delivery-bound dependencies. Any
additional change to experimental policy injection must be tracked explicitly,
not silently attributed to the already-published `rules.list` change.

Primary fixtures: legacy `tests/learning-hints-on-demand.test.ts` and
`tests/learning-turn-repair.test.ts`; port their source behavior through the Go
application tools, `replyplan` evidence budgeting and actual model requests.
For the separate experimental path, retain `TestExperiencePlannerActualSourceOrder`,
`TestRuntimeHintsNativeEndToEnd`, `generate-experience-planner.mjs` and
`generate-experience-runtime-fixtures.mjs`, including `RuntimeHints.DeliveredTool`,
`RecordUse` and `Sent` feedback linkage.

Required evidence: ordinary learned hints are neither read nor serialized during
initial preparation; `rules.list` remains available and can narrow a query;
whole-receipt withholding attaches no undelivered dependencies; a delivered
receipt attaches only its own source dependencies; revocation/recall remains
valid; background learning does not block foreground HTTP or Replyer; queued is
not reported as persisted learning. Measure actual request bytes before/after on
the same conversation and test experimental policies separately.

### P1-B: use memes naturally when appropriate

Current `memeengine/tools.go::MemeTools` advertises `memes.search`/`memes.feedback`.
Planner prompts permit meme choices and require actual search receipts, but do
not give a clear positive policy for naturally initiating a relevant meme search.
`botruntime/send.go::validateSegments` requires media assets in the chosen plan.

Target: explicitly allow/encourage context-appropriate meme lookup and selection,
without requiring the user to say “send a meme”, and without forcing a meme into
every reply. Reuse the Planner's bounded read-tool loop: show real candidate IDs
and meaning, allow query refinement, then pick actual media, independently useful
text, or no reply. Do not describe an unsent meme as a replacement. An unselected
candidate becoming unavailable must not block the selected output. Update source
prompts and generated runtime contracts together.
Keep asset existence, permissions, source availability and outgoing-media
validation; never fabricate an asset or infer positive feedback from silence.

Required fixtures: natural light conversation searches/selects/sends a real
available fixture asset; unsuitable serious context chooses text; no match does
not fabricate; permission revocation/recall before transmit prevents delivery;
search result selection survives the Planner-to-Replyer handoff. Exercise the
actual adapter receipt path, not only prompt-string assertions. Landing points:
`botruntime/testdata/contracts.json`, `replyplan`, `memeengine`, `memeservice`,
source prompt generators and existing Meme outgoing/native integration tests.

### P1-C: preserve a completed reply; Planner explicitly cancels

Current `botruntime/send.go::validateSend` passes exact proposed segments and new
messages to `reply.validate`, initializes validity to false, and aborts on missing
approval, false, or another topic-version change. `replyer.go`/`turn.go` have no
cross-turn pending-draft owner. This is not the requested cancellation-only model.

Target: once Replyer has a complete outgoing draft, retain it. Send directly
when there is no new same-topic message; only new same-topic ingress triggers
Planner context refresh. Give the Planner that exact previous draft;
its decision about that draft is cancellation, not rewriting/re-generating it.
After a normally completed review round, absence of explicit cancellation starts
sending the retained draft. Review has an independent Planner budget, not the
remaining Replyer generation deadline. Record provider failure or deadline expiry
as a failed review, not approval. In that case, release the unchanged draft only
if its hard send eligibility still holds; do not loop indefinitely waiting for
model approval. Specify draft identity, ownership, review generation and the
single-send transition before implementation. Temporary state may survive the
original model generation but must not automatically replay after restart.

Repeated unrelated ingress must not discard or indefinitely restart the same
completed draft. Arrivals after the review snapshot belong to the next round.
Recalling unrelated recent messages or unselected candidates must not discard a
draft; recalled quoted targets or explicitly referenced required evidence still
invalidate it. Explicit cancellation must address the exact draft identity. Source
recall, revoked permissions, scene shutdown and uncertain transport outcomes are
separate lifecycle/authority events, not approval defaults to bypass. Already
sent/unknown segments must never be replayed after interruption or restart.

Landing points: `botruntime/send.go`, `replyer.go`, `turn.go`, ingress/admission,
turn cleanup and `dispatch`; replace the source validation tool/prompt contract
and regenerate `generate-bot-runtime-fixtures.mjs` intentionally.
Reuse `TestActualRuntimeTurnOracle` (especially staleValidation),
`TestTransmitGateRechecksAfterAsyncAdapterPreparation`,
`TestCancelUnknownTransmissionNeverRetriesOrLosesProtection` and
`TestOrderedEditsNeverUpgradeOldSnapshot`.
New fixtures must cover draft-before-interruption, review without cancellation,
explicit cancellation, repeated ingress during review, independent review timeout
and provider failure, partial send, unknown receipt, recall/permission change and
shutdown. Assert exact payload
preservation and at-most-once physical transmission, not only final status.

## P2: coverage closure and integration

- **REST/UI/session transport:** inventory frozen OpenAPI operations against
  `internal/httpapi`, actual `app` constructors and frontend calls. Mark each as
  implemented/tested, unsupported or intentionally changed, including auth,
  pagination, errors, writes and streaming. Add real TCP fixtures for gaps.
  Audit session/WebSocket upgrade, HTTP 101, disconnect/cancel and reconnect
  semantics separately. Do not call partial admin-route coverage complete.
- **V1/V2 and builtins:** retain lazy shared Node, actual isolate unload, original
  callback/protocol identity and physical drain. Audit all tool/cache/send/model/
  adapter capabilities against actual plugin consumers. Four WASM manifests do
  not establish all four complete products. `app/web_read_disabled.go` still
  rejects default admission; the isolated `web_read_candidate` path cannot be
  promoted before its transport/HTTP 101 and shutdown semantics are accepted.
- **Plugin API V1/V2 badge acceptance:** `app/pages/plugins.vue` contains
  `V{{ plugin.apiVersion ?? 1 }}` and `pluginmanager.Manager.List` emits version
  metadata; `httpapi/plugin_typing_test.go` checks V2 metadata for typing-delay.
  These are source-level observations, not rendered UI acceptance. Add a named
  Plugin API badge check covering one V1 local plugin and one V2 builtin through
  the real list API and UI: correct V1/V2 label, legacy omitted-field V1 fallback,
  independence from the plugin's own release version, and stable display across
  enable/disable/reload. Run contract checks and capture the actual rendered list;
  do not mark the mixed-version UI check complete from metadata alone.
- **Live graph:** verify media ingestion/history/cache, workspace/semantic and
  Experience tools, proactive timers, settings effects and source-wide recall
  ordering against current code and actual-source fixtures. Several historical
  scope gaps have since gained implementations; enumerate missing connections
  rather than re-porting modules merely because an old document says pending.
  Include real controlled platform ingress/send/reconnect coverage separately
  from synthetic protocol fixtures, with no unrequested external sends.

## P3: cutover gates, independent of package CI

Use disposable copies with matching authority; never test migration by opening
production data with the candidate. Establish fresh install, existing-data open,
old backup restore, corruption/intent-ACK gaps, cancellation and restart behavior
through the complete packaged application. Preserve credentials, IDs, UTF-16/JSON
semantics, media, source tombstones and no restart replay. Document operator
rollback/recovery behavior and its limits before selecting the Go default.

Run integrated HTTP/worker contention with slow clients, concurrent ingress,
model work, learning, recovery reads and shutdown. Record p50/p95/p99 latency,
CPU/RSS/allocations, lock waits, history scan counts and cold/warm plugin startup
on stated hardware/data sizes. First-open/external-change linear audits and
historically slow SnowLuma WASM compilation require actual measurement; previous
package durations and green race tests are not performance acceptance.

Switch production startup only after P0-P2 and this gate pass on one exact head,
with the API/platform matrix, reproducible release assets and rollout/recovery
instructions reviewed. No percentage-complete estimate substitutes for that gate.

## Verification commands and evidence policy

Provision pinned dependencies as `.github/workflows/golang.yml` does (Node
24.22.1, lockfile, native ICU/image inputs); reuse prepared environments rather
than silently skipping native tests. For code slices, run affected package race
and vet checks first, then the existing aggregate profile:

```sh
make -C backend plugin-layout-check
make -C backend test differential test-bundle bundle test-sqlite-profile test-projection-source-free installation-bundle-check
make -C backend native-icu-build native-icu-test native-icu-source-free
npm run contract:check
npm run typecheck
npm run build
```

Use the workflow's native-image and numeric checks with their required environment
as well. `native-icu-test` includes the mandatory native runtime-preview gate;
ordinary tests deliberately do not prove the native startup profile. New packaged,
behavior and load fixtures above must be added, not assumed covered by these
commands. Record exact commands, failures, skips, reruns and SHA. A documentation
commit does not inherit a new behavioral pass merely because its parent was green.
