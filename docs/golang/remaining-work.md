# Go migration remaining work and handoff

Baseline reviewed: `feat/refactor-golang` at
`58e48dc6d3727abc68c3bcc4b1917b54f69acbb9` (2026-11-11).
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

The user requested these changes on 2026-11-17. Any work on the old TypeScript
server is independent and not evidence they are released or ported to this Go
baseline. Pin the eventual TS behavior commit when it is actually published.

### P1-A: learning advice on demand, learning work off the reply path

Current chain: `app/experience_planner.go::experiencePlanner` calls
`experienceservice/runtime_hints.go::PlannerContext`; `botruntime/planning.go`
serializes the result into `policyContext`. This currently selects/deduplicates at
most three experience hints; it is **not a whole-library dump**. Separately,
`botruntime/prepare.go` collects extension context into `learned` evidence.

Target: no automatic advice-body injection into every Planner request. Expose
relevant advice through an on-demand model tool; keep proposal/consolidation work
in the existing background learning owners. A tool read and a background learning
job are distinct operations. Preserve exact source eligibility, scene/privacy
scope, exposure receipts and feedback attribution. Record exposure when advice is
actually delivered, not merely when a hidden prefetch occurs. Do not fix context
cost by silently dropping learned information or adding arbitrary size caps.

Landing points: the two chains above; `internal/learningowner`, `learningruntime`,
`contextuallearning`; `RuntimeHints.DeliveredTool`, `RecordUse` and `Sent` linkage.
Reuse `TestExperiencePlannerActualSourceOrder`,
`TestRuntimeHintsNativeEndToEnd`, `generate-experience-planner.mjs` and
`generate-experience-runtime-fixtures.mjs` with intentional contract updates.

Required new evidence: no hint body in an untouched Planner request; on-demand
query returns relevant scoped advice; delivery records exactly one exposure;
revocation/recall rejects stale receipts; background learning does not block
foreground HTTP or Replyer; queued is not reported as persisted learning.
Measure actual serialized request bytes before/after on the same conversation.

### P1-B: use memes naturally when appropriate

Current `memeengine/tools.go::MemeTools` advertises `memes.search`/`memes.feedback`.
Planner prompts permit meme choices and require actual search receipts, but do
not give a clear positive policy for naturally initiating a relevant meme search.
`botruntime/send.go::validateSegments` requires media assets in the chosen plan.

Target: explicitly allow/encourage context-appropriate meme lookup and selection,
without requiring the user to say “send a meme”, and without forcing a meme into
every reply. Update source prompts and generated runtime contracts together.
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

Target: once Replyer has a complete outgoing draft, retain it while new ingress
triggers Planner context refresh. Give the Planner that exact previous draft;
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
completed draft. Explicit cancellation must address its exact identity. Source
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
