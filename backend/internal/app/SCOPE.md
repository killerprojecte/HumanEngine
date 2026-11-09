# Native application composition checkpoint

This package composes actual native owners for integration and explicit future
runtime-preview use. The command entry point is unchanged: `--read-only-preview`
remains read-only, and the default command does not start this application.
This is not a claim of complete TypeScript Engine compatibility.

## Composed and exercised

- Installation, separate recovery authority, main SQLite database, credentials,
  pinned ICU/collation, native image codec and physical model budget service.
- HTTP settings/models, scenes, plugins, Meme, habits, Experience administration,
  diagnostics and model-call queries through their concrete stores and gates.
- Shared V1 bridge/manager host, actual adapter ingress, native model protocol,
  model-facing tool execution, delivery, disable, shutdown and restart without
  replay. No substitute JavaScript Engine or fake adapter transport is used.
- Model configuration is captured once per foreground Router/turn. An HTTP model
  update affects the next turn while accepted stages and continuations retain
  their Router. Cross-Router continuation ownership remains enforced.
- Actual semantic memory, ordinary learning, projections, working sets,
  commitments, relations and knowledge dependencies. Commitment/relation base
  schemas are initialized through their real constructors' Initialize methods.
- Model-facing `memory.search`, `memory.person` and `memory.identities`, including
  actual permission checks, original receipt identity, deferred planner selection,
  memory delivery tracking, identity state validation and scoped cached guards.
  Model-read capture is registered once by Meme; memory and identity compose
  with existing hooks rather than replacing them.
- Experience lifecycle and planner context gate/capture, actual HTTP administration,
  retained source recall, same-writer invalidation and physical callback drain.
- The source default `learningEnabled=false` scene update is a normal successful
  HTTP path. Experience pause precedes runtime scene registration; semantic
  configuration precedes ordinary-learning, semantic, contextual and media-task
  cancellation. The application owns these effects once, avoiding duplicate epochs.
- Contextual proposal owner and keyed media-task lifetime are real owned services,
  with edit/recall/scene cancellation and physical shutdown joins. No media-ingest
  producer is currently installed in this graph, so this is not media processing.
- Async structured logs retain request/scene/turn/stage identifiers and controlled
  failure codes. Raw internal errors are not serialized into plugin events or logs.
- Startup failure cleanup, uncancelled held HTTP shutdown, accepted runtime/provider
  cleanup and database-last close are tested. A failed drain does not close borrowed
  dependencies or claim success; the application remains available for retry.

## Remaining application boundaries

- No runtime command launcher is exposed yet. A future explicit runtime-preview
  option must define installation/configuration inputs without changing read-only
  preview semantics or silently selecting this graph as the default backend.
- Remaining source model tools, complete prompt/manual-rule composition and all
  exposure domains are not wired. Public Historical/Commitment owners are separate
  components until explicitly composed here. Relation, commitment current/history/
  timeline, historical, workspace, knowledge and semantic exposure guards are not
  implied by memory/identity guards. Contextual `learning.propose` is not yet a model
  tool in this graph. Complete Experience model-facing tools are also outstanding.
- Source-wide recall preparation ordering (including every memory/habit domain),
  proactive scene timer binding and complete global Engine startup hooks remain.
- Four real built-in WASM artifacts are built and required by the native test
  profile, but this graph's V1 integration does not prove all four built-ins have
  their complete product capabilities installed. Native OneBot and full SnowLuma
  transport/history/media integration remain separate work.
- Native media ingestion, historical image transport, private media cache/fetch and
  the unresolved HTTP 101 behavior are excluded. No legacy readImage bridge or
  permissive media owner is substituted.
- The existing async Logger is used; complete source log retention/status/delivery
  route equivalence is not claimed. Independent read publication still preserves
  recovery checks; no constant-time cross-connection audit performance claim is made.
- Lazy security schema initialization covers SaveModels and its write-only secret
  mutations. It is not generalized to every standalone credentials/settings writer.

## Evidence and limits

The frozen memory graph r5 passed actual four-WASM build, all ten native app tests
without skips, race instrumentation, three-package vet and unchanged input checks
(session 32053). This includes real memory/identity selection through model send,
HTTP model replacement, recall, disable/restart, Unicode provider payload, startup
failure and held-HTTP drain. Default logical tests and runtime diagnostic tests
passed in r4 (session 27784); five native-only app tests explicitly skipped in that
profile and are not counted as default behavioral passes. The earlier native r4
identity nested-publication timeout is retained as failure evidence; its standalone
three-file repair passed source/Identity race/vet (51529) and was published separately.

These results belong to those exact frozen inputs. A new assembly on the latest
public plugin-directory layout requires its own merged compile/native graph check.
Package test durations are not HTTP latency or migration performance benchmarks.
