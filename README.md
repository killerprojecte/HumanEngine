# HumanEngine

A self-hosted, single-character conversational runtime. Nuxt UI provides a calm administration workspace; scene-scoped learning keeps private and group experiences separate.

## Docker Compose

Use `ghcr.io/killerprojecte/humanengine` with the included `compose.yaml`. Copy `.env.example` to `.env`, configure the local administration token, then run `docker compose pull && docker compose up -d --no-build`. The default published address is `http://127.0.0.1:3000`; configuration and media persist in a named volume. Private GHCR pulls may require your existing authorized registry login. See [container setup, verified tags, persistence and access](docs/containers.md).

## Run locally

Requires Node.js **24.22.1 or later in the 24 LTS series**, npm, and a local filesystem for SQLite. Native SQLite/vector packages must support your operating system and architecture; Linux x64 is the initial validation target.

```sh
npm ci
cp .env.example .env
# Set HUMANENGINE_ADMIN_TOKEN to a long random value in .env.
# Enter provider API keys directly in the UI after startup.
npm run dev
```

Open `http://127.0.0.1:3000`. Enter the server's administration token. The token remains in tab memory, not local storage. Configure a provider using either **OpenAI Chat Completions** or **OpenAI Responses**, create independent model instances, then assign the ordered Planner and Dialogue routes (`planner` and `thinker` IDs remain unchanged). Model names can be typed manually even when discovery is unavailable. Save providers before discovery.

```sh
npm run check
npm run build
# Production does not automatically load .env. Supply environment variables:
HOST=127.0.0.1 node --env-file=.env .output/server/index.mjs
```

No public deployment is performed by this project. Keep the interface on loopback or a trusted private network; use TLS and an authenticated reverse proxy before remote access. Do not expose the management token or provider keys to an untrusted plugin or page.

## Implemented vertical slices

- Nuxt 4 + Nuxt UI 4, theme customization rather than a separate component system.
- English, Simplified Chinese, and Traditional Chinese locales.
- SQLite WAL configuration persistence and stable sqlite-vec extension loading.
- Server-side schema validation produces `openapi.json`; `openapi-typescript` produces the browser's typed client contract. `npm run contract:check` rejects drift.
- Provider identity, model-instance identity, and ordered role routes are separate. Duplicate API model names across providers are supported.
- Both OpenAI protocols support tool roundtrips, cancellation, usage, safe errors, model discovery, and explicit embedding calls.
- Independent participation and learning switches; no proactive private messaging.
- Administration bearer authentication, no secret values in configuration responses, bounded technical events.

The runtime starts with the server, independently of browser visits. Enable the SnowLuma plugin after setting its OneBot WebSocket URL, bot account ID, and access token directly in the UI. Incoming scenes start disabled; explicitly enable participation and/or learning in Scenes.

- Planner participation, bounded research and grounded response plans, followed by Dialogue composition with one plan-selected delivery tool. Independent routes, scoped evidence, cancellation, receipt-aware segments, and stale-answer validation remain enforced; see [Planner and Dialogue](docs/planner-dialogue.md).
- Isolated Web simulation and read-only real history preview with explicit partial/unavailable coverage.
- Scene/person memory, conservative public-technical promotion, sourced background learning, correction/lock/delete guards, and scoped sqlite-vec search.
- Durable recall tombstones, in-flight cancellation, derived-source invalidation, and no history resurrection or restart replay.
- Original GIF/WebP storage, ordered changed-frame vision analysis, metadata search, conservative automatic library/privacy selection with manual precedence, and sourced feedback.
- Sourced low-weight habits, structured shared expression preferences, manual capability probes, optional price estimates, priority scheduling, and soft participation windows.
- Trusted local plugins: schema forms, custom pages, scoped adapter and model-protocol registration, readonly events, and guarded send tools/hooks with cancellation-safe disable.

No real provider account or QQ/SnowLuma connection has been exercised. Integration tests use local HTTP/WebSocket fixtures; animated-media tests use actual encoded GIF/WebP fixtures. The dashboard has been exercised in the supported cloud browser with safe synthetic data, including real edit/reset/save interactions; see [browser verification and fixes](docs/ui-verification.md). Production HTTP smoke and Nuxt type/build checks are separate verification. See [verification and remaining work](docs/implementation-status.md) and the [completion matrix](docs/completion-matrix.md).

## Development

The server schemas in `src/schema.ts` and route definitions in `src/api.ts` are the contract source of truth. After API changes run `npm run contract`, then `npm run check` and `npm run build`. Do not edit generated files. See [model protocols](docs/model-protocols.md), [SnowLuma setup](docs/snowluma.md), [memory](docs/memory.md), [memes](docs/memes.md), [learned habits](docs/habits.md), and [architecture boundaries](docs/architecture.md), [trusted plugins](docs/plugins.md), and [backup/restore and benchmark operations](docs/operations.md).

Secrets entered in the UI are encrypted in SQLite with a separate automatically generated installation key; legacy environment references remain optional. See [credential storage and recovery](docs/secrets.md). Configuration changes apply to new turns. Temporary chats, pending sends, attention, and simulation sessions are never restored after restart. Recall tombstones are durable provenance metadata, not an outgoing queue.

Dependency audit limitations and the tested esbuild override are documented in [SECURITY.md](SECURITY.md). The remaining upstream node-forge advisory is not represented as a clean audit.
