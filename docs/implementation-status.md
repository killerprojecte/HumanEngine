# Implementation and verification status

## Implemented

The runtime, management API, and dashboard are integrated, not independent demo stubs. Both OpenAI protocols are exercised against HTTP fixtures; the full authenticated API → Planner → Thinker → send-tool simulation path is covered by an integration test. OneBot transport tests exercise actual WebSocket events, history, recalls, unknown send outcomes, and disposal. SQLite and sqlite-vec are actually loaded in tests; GIF/WebP fixtures actually contain multiple animated frames.

Configuration, scene participation/learning, models/routes, chat simulation/preview, memory management, manual rules, persona version records, media management, and plugins have typed administration APIs and Nuxt UI pages. Schemas generate OpenAPI and browser types; contract drift is checked.

## Verification boundaries

- No live provider credentials or QQ account were supplied. Live provider behavior, SnowLuma deployment compatibility, client animation playback, and reconnect behavior against a real QQ session remain unverified.
- The earlier shell Chromium launch failed with a process-singleton socket `EPERM`; no browser flags or permissions were changed. A later supported cloud CDP browser run reached the local production app through the normal desktop Terminal and verified synthetic UI interactions/screenshots. See [browser verification](ui-verification.md). This does not establish live provider/QQ quality or exhaustive device/accessibility coverage.
- The initial npm attempt failed because its default user-level npm cache did not exist. The configured cache is in an explicitly writable workspace directory. TypeScript 7 conflicted with the contract generator's peer range; the latest compatible TypeScript 5.9.3 is pinned instead of forcing resolution.
- The tsx CLI attempted an unavailable Unix IPC socket. Test scripts use the supported `node --import tsx` loader, which does not need that CLI IPC channel.
- Native dependencies are validated on Linux x64 / Node 24. Other deployment architectures need their own install and smoke check.

## Current runtime and operations

- Sourced scene habits and structured shared bot-expression preferences are integrated with learning, retrieval, feedback, recall, and lock/delete administration. Background batches first extract from sources alone, then use scoped related memory only for candidate-index reconciliation and explicit name/correction supersession. Exact normalized content can share alternative proofs; differing semantic paraphrases may remain separate for safe lineage. Conservative original-context technical review can promote the same sourced record without increasing factual confidence.
- Planner may assign one message to several topics and select a main topic. Thinker context narrows to that topic; no conversational-quality or human-likeness benchmark is claimed.
- Participation-window inclinations are soft hints in the instance timezone. Bounded priority scheduling favors foreground decisions over queued background work; it is not a spending limit.
- Automatic animated-media intake connects ordered distinct GIF/WebP samples to vision tags, conservative availability/sharing decisions, metadata search, and original-byte guarded sends. Manual choices and source recalls remain authoritative; bounded sampling and model privacy review are not guarantees.
- Explicit single-model capability probes never silently alter configuration. Vision probes establish input acceptance, not image understanding.
- Daily usage stores returned token counters and optional request-time configured price estimates. Missing counters, unknown prices, and failed attempts are unpriced, not free; counters are not a provider invoice and canceled/unknown outcomes may still be billed.
- Technical events have bounded disk rotation without raw chats. Optional visible model previews are disabled by default, bounded in memory, secret-redacted, and cleared on recall, disable, or restart. Hidden protocol reasoning state is never previewed.
- Consistent SQLite backup/fresh restore, scoped vector benchmarks, read-only versioned plugin events, and cancellation-safe plugin lifecycle are implemented.

## Extension SDK and remaining validation

Trusted local plugins can register namespaced model protocols and platform adapters, with engine-local registries, scoped ingress/history/recall, and lifecycle-bound cancellation. Protocol continuation stays private to a model instance and registration generation. Declared send tools use the same core checks as builtin sends, including exact-segment revalidation after hooks and before each segment; invocation completion revokes retained send callbacks. Anthropic and NapCat implementations are intentionally not included.

The overview exposes actual registered adapter status, and scene inspection exposes body-free temporary topic/task state. Bot mentions and history identities are normalized for the Planner; attention decays with time, not with dashboard read frequency. These measured activity signals do not imply real feelings or a human life.

Representative conversation replay, real provider/QQ compatibility, exhaustive browser/device/accessibility coverage, and target-machine capacity testing remain external validation work, not successful checks. See operations documentation for measured synthetic vector scope, mixed latency results, and limits.

No fees are capped, no private conversations are proactively initiated, and no pending sends are restored. Missing or offline recall notifications cannot be detected retroactively without platform evidence.

See [completion scope and evidence](completion-matrix.md) for implemented paths, deliberate limits, observed browser QA, and deployment-side verification needs.
