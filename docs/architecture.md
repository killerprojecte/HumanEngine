# Architecture and non-negotiable boundaries

One process hosts a single character, SQLite state, a TypeScript bot runtime, and a Nuxt administration interface. Closing the browser does not stop the bot. There is no enterprise tenancy layer, marketplace, or untrusted-code sandbox.

## Conversation pipeline

Platform event → scene/topic batching and attention → Planner participation decision → Thinker tools → guarded sequential sends → receipts. Planner decides silent, wait, or reply, never authors the answer. Thinker final text is not automatically forwarded after sending tools. Errors stay in technical logs, not chat.

Each turn snapshots role templates, manual rules, settings, and model routing. Participation disable and source recall are immediate exceptions: cancel dependent unsent work, never pretend sent segments were not sent. Private conversations only respond; proactive group turns must have a current contextual motive.

## Persistence and privacy

Configuration, scene settings, learned records, rules, media metadata, and provenance are persistent. Raw chats are optional bounded process memory (default rolling 24 hours), not permanent logs. Pending sends, active topics, model requests, attention, and simulation history are transient. There is no restart replay/outbox.

Person memory uses `(sceneId, personId)`, never a global identity profile. Shared public knowledge excludes private project terms or person facts. Bot expression tastes and generic safe memes may be shared, but their private provenance must not leak into other scenes.

Simulation reuses the real decision/tool pipeline with isolated context: no real conversation reads, no persistent learning writes, no platform sends. Its transient state is discarded on clear/end. Technical counters may be recorded without its body.

## Recall semantics

A recall event identifies account, scene, and opaque message ID. Remove cached body, retain unavailable status, and prevent history backfill from restoring it. Durable minimal tombstones prevent a recalled source being relearned after restart; they do not restore conversation state.

Cancel or reassess only work depending on that source; unrelated work may continue. Recheck before every send segment and before learning commit. Already-sent bot messages are not automatically recalled or repeated. Remove pending learning candidates. Derived facts/rules/media with no independent evidence become unavailable; with other independent sources, remove the recalled source and reassess. Recall means unavailable evidence, not proof that its content was false. Shared assets with independent sources are not blindly deleted.

Adapters cannot know recalls that were never delivered while offline. Document these gaps rather than promising universal detection.

## Models and plugins

Default protocols are OpenAI Chat Completions and OpenAI Responses. Anthropic can be a later protocol plugin. Instances have stable IDs separate from API model names and provider IDs. Fallback order is explicit; embedding spaces never mix across fallback providers.

Only trusted local TypeScript plugins are supported initially. Read-only listeners cannot mutate events. Ordered intervention hooks can cancel terminally; exceptions fail closed before sends. Disable must stop callbacks, unregister tools, abort pending work, and dispose resources. Plugin schemas support standard forms; custom management pages use the authenticated backend, never direct database/key access.

## Dependency selection

Stable registry versions are checked at implementation start and pinned with a lockfile. Node 24 LTS is the runtime target. sqlite-vec 0.1.9 is used, not the 0.1.10 alpha channel. The latest TypeScript 7.0.2 conflicts with openapi-typescript 7.13.0's `^5.x` peer; use the latest compatible TypeScript 5.9.3 rather than forcing an invalid dependency tree.

## Delivery order

1. Configuration, both model protocols, OpenAPI/client, SQLite, Nuxt UI/i18n, status/logs.
2. Isolated simulator and send/decision runtime; OneBot abstraction and SnowLuma adapter.
3. Scoped memory, learning, recall provenance, tools, rules, vector search.
4. Meme analysis/reuse, trusted plugin lifecycle and administration extensions.
5. Focused fault/integration/contract checks, build and operational documentation.
