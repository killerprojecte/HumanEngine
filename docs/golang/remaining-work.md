# Go migration remaining work and handoff

Status reviewed at Go head `13e8510`. The frozen compatibility oracle remains
master `24fe8a773d87f2f09996e259fca46524c2c02928`, as recorded in
`backend/compatibility/baseline.json`. Newer legacy references are intentional
behavior changes, not replacements for that baseline. This is not a cutover claim.

Historical [baseline CI 43051645297](https://github.com/killerprojecte/HumanEngine/actions/runs/43051645297)
passed the compatibility job. This documents those tested boundaries, not
whole-product parity. No behavioral tests were rerun for this documentation update.

## Current executable truth

`backend/cmd/humanengine/main.go` exposes an explicit **writable
`--runtime-preview`**, implemented in `runtime_preview.go`. It opens the actual
`internal/app` owners and has native subprocess startup, held-request shutdown,
restart and duplicate-writer tests. It is not merely a collection of packages.
`--read-only-preview` remains read-only; the normal command still refuses to
replace production. See [runtime preview](../../backend/docs/runtime-preview.md).

Production startup remains unchanged. Hosted package tests exercise the opt-in
Go image with read-only repository permissions; they do not publish or deploy
it. There is no source-backed public TypeScript browser WebSocket/HTTP 101
contract to port: the logs UI polls HTTP. Outbound optional WebRead HTTP 101
handling and physical disable are separately tested under the tagged candidate;
WebRead remains disabled in the default build.

The native application already assembles real installation/recovery, providers,
Planner/Replyer/send, V1 host, memory/knowledge/relation/commitment owners, learning,
Meme/Experience services and multiple admin routes. Exact endpoint and platform
coverage must be enumerated against OpenAPI and real owner tests before claiming
full compatibility. Do not infer coverage from a directory, constructor or green
package suite.

## Delivery order

1. **P0: requalify the packaged preview and rollback rehearsal** on the exact
   integrated release candidate. Keep the production default unchanged.
2. **P1: retain regression coverage for the three implemented runtime changes**
   below, separately from the frozen baseline. The follow-up quote and typing
   generation cases are covered by the Go regressions in `35e7556` and `13e8510`.
3. **P2: close the remaining platform and plugin acceptance gaps** with named
   real-boundary tests.
4. **P3: qualify broader migration, restore and load** before any cutover.

For each change, record its source or product contract, commands actually run,
remaining omissions and exact SHA.

## P0: hosted evidence and remaining package gate

The [opt-in Docker/Compose recipe and acceptance driver](../../backend/packaging/runtime-preview/README.md)
exercise the actual packaged application, not a health stub. Two hosted runs
closed distinct synthetic boundaries on different commits:

- [44836458981](https://github.com/killerprojecte/HumanEngine/actions/runs/44836458981)
  at `de94427`: real same-origin Nuxt/Chromium login, dashboard, settings write,
  reload and logout against the package, alongside the normal package smoke.
- [44849902431](https://github.com/killerprojecte/HumanEngine/actions/runs/44849902431)
  at `9e53c0e6`: actual TS → Go → TS on a disposable matching data/authority
  pair, plus old-backup restore checks for flags, deletion and hold.

Neither certifies the current head. Run the required gates on one exact
integrated candidate and retain the image/commit identity and evidence. Browser
and restore rehearsals are scoped opt-in jobs, not coverage on ordinary pushes.
Do not publish a distributable image until the independent
[native image redistribution gate](../../backend/internal/imagecodec/distribution/RELEASE-GATES.md)
is closed.

## P1: updated runtime semantics implemented in the Go candidate

The user requested these changes on 2026-11-17. Legacy behavior commit
[04b1817](https://github.com/killerprojecte/HumanEngine/commit/04b1817af8055d58021c85e94d1f72be914125d6)
is now on `master`; the tested reference is its successor
[b7f3f92](https://github.com/killerprojecte/HumanEngine/commit/b7f3f92b0d27168955f507535e38c00ed15c6826),
which makes the typing-cancellation test explicit. These behavior fixtures define deliberate deltas from the frozen `24fe8a77`
compatibility baseline. The Go candidate wires each delta into its real runtime
and provides tagged Go acceptance cases; integrated native CI and the packaged
trace remain separate gates. This is not a production cutover.

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

The Go application registers the ordinary on-demand contract in
`internal/app/rules_tools.go`, with a whole-result receipt budget in
`internal/replyplan/budget.go`. Generic extension context preparation remains
available for other evidence owners.
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

The frozen baseline `memeengine/tools.go::MemeTools` advertises `memes.search`/`memes.feedback`.
Planner prompts permit meme choices and require actual search receipts, but do
not give a clear positive policy for naturally initiating a relevant meme search.
`botruntime/send.go::validateSegments` requires media assets in the chosen plan.

The current Go delta allows context-appropriate meme lookup and selection,
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

The frozen `botruntime/send.go::validateSend` passed exact proposed segments and new
messages to `reply.validate`, initialized validity to false, and aborted on missing
approval, false, or another topic-version change. `replyer.go`/`turn.go` had no
retained-draft owner. That is not the requested cancellation-only model.

The Go runtime now retains the completed draft in the current send guard
(`botruntime/retained_draft.go`). It keeps the exact operation ID and a copy of
the segments, keyed to the turn's draft ID. If there is no new eligible
same-topic ingress, it proceeds through the ordinary send eligibility checks
without another model call. Otherwise it takes one review snapshot and gives
the Planner the unchanged draft on an independent, hard Planner deadline.
Only a valid `reply.cancel` for that draft cancels it; another arrival cannot
start another review or regenerate the reply. A provider error or review
timeout is not approval: the unchanged draft proceeds only if current send
eligibility still holds. No draft is written for restart replay.

`TestRetainedDraftOneReviewAndIndependentBudget` asserts exact segment payloads,
one Replyer generation, and zero or one review for these paths, including
wrong-draft cancellation, further ingress during review, timeout/provider
failure, recall, scene cancellation and authority errors.
`TestRetainedDraftRecallOnlyReferencesAtCompletedBoundary` distinguishes unrelated
recall from cited evidence and quoted targets. The retained-draft transmit
timeout and ingress-during-send tests cover unknown receipts without replay and
in-flight multi-segment completion; the grounding-repair test covers the
original generation deadline. The dispatch-facing tests separately check
late authority loss after async adapter preparation, unknown transmission
on cancellation, and ordered edits not upgrading old snapshots.

The [installed SnowLuma test at `4c81311`](https://github.com/killerprojecte/HumanEngine/actions/runs/44900131187/job/136834087916)
covers a three-segment draft across a real loopback WebSocket disconnect and
reconnect: the first segment is acknowledged, the second is transmitted but its
receipt is unknown, and the third is not sent. No old segment is replayed and a
fresh message succeeds on the replacement connection. Combining a partial ACK
with process shutdown or with recall/policy changes between segments remains
unproven. Check exact payloads and physical send counts in those cases.

## P2: coverage closure and integration

- **REST/UI/session transport:** retain exact endpoint and frontend coverage
  rather than inferring parity from OpenAPI or package tests. The authenticated
  Go browser supports scene state, message preview, local retained history and
  default remote history (`TestLocalHistoryActualSource`,
  `TestRemoteHistoryActualSource`). Implemented image scopes are managed outgoing
  originals, retained native SnowLuma inline images and opaque-token inline
  results, and qualified retained direct public HTTP(S) originals
  (`TestManagedDisplayImageActualSource`, `TestSnowLumaInlineImageComposed`,
  `TestTokenImageNativeApplicationProof`, `TestDirectPublicImageNativeApplicationProof`).
  Later scoped proofs cover a retained opaque token returning a public HTTP(S)
  URL, remote SnowLuma history inline or opaque originals, and direct-URL 403
  fallback to an independent retained token. These do not establish arbitrary
  adapter coverage, retries for other statuses or complete vision parity.
- **V1/V2 and builtins:** retain lazy shared Node, actual isolate unload, original
  callback/protocol identity and physical drain. Verify remaining capabilities
  against actual plugin consumers. The tagged outbound WebRead transport and
  disable tests do not enable its default admission or prove public browser
  upgrade behavior.
- **Plugin API V1/V2 badge acceptance:** the packaged
  [Chromium proof at `72a1d2f`](https://github.com/killerprojecte/HumanEngine/actions/runs/44876648329/job/136762697079)
  checked rendered V1 badges for two local plugins and a V2 badge for a
  built-in through the real list API. This is focused browser evidence, not
  browser coverage for every package commit or arbitrary plugins.
- **Live graph:** see [the native application scope](../../backend/internal/app/SCOPE.md)
  for installed known-only SnowLuma intake, stored tag reuse, the group proactive
  timer, same-writer recall including habits and read-only Experience feedback.
  Keep automatic sticker analysis, broader history/cache integration,
  workspace/semantic exposure, Experience writes/trials and remaining startup
  and settings effects separate. Controlled third-party platform acceptance is
  distinct from loopback protocol fixtures; do not send unrequested traffic.

## P3: cutover gates, independent of package CI

Use disposable copies with matching authority; never test migration by opening
production data with the candidate. Extend the proven synthetic round trip and
old-backup cases to broader data, failure and rollback coverage: fresh install,
existing-data open, corruption/intent-ACK gaps, cancellation and restart through
the complete packaged application. Preserve credentials, IDs, UTF-16/JSON
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
