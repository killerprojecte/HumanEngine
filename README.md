# HumanEngine

A self-hosted, single-character conversational runtime. Nuxt UI provides a calm administration workspace; scene-scoped learning keeps private and group experiences separate.

## Run locally

Requires Node.js **24.22.1 or later in the 24 LTS series**, npm, and a local filesystem for SQLite. Native SQLite/vector packages must support your operating system and architecture; Linux x64 is the initial validation target.

```sh
npm ci
cp .env.example .env
# Set HUMANENGINE_ADMIN_TOKEN to a long random value in .env.
# Set provider secrets in .env, referenced by variable name in the UI.
npm run dev
```

Open `http://127.0.0.1:3000`. Enter the server's administration token. The token remains in tab memory, not local storage. Configure a provider using either **OpenAI Chat Completions** or **OpenAI Responses**, create independent model instances, then assign the ordered Planner and Thinker routes. Model names can be typed manually even when discovery is unavailable. Save providers before discovery.

```sh
npm run check
npm run build
# Production does not automatically load .env. Supply environment variables:
HOST=127.0.0.1 node --env-file=.env .output/server/index.mjs
```

No public deployment is performed by this project. Keep the interface on loopback or a trusted private network; use TLS and an authenticated reverse proxy before remote access. Do not expose the management token or provider keys to an untrusted plugin or page.

## Foundation

- Nuxt 4 + Nuxt UI 4, theme customization rather than a separate component system.
- English, Simplified Chinese, and Traditional Chinese locales.
- SQLite WAL configuration persistence and stable sqlite-vec extension loading.
- Server-side schema validation produces `openapi.json`; `openapi-typescript` produces the browser's typed client contract. `npm run contract:check` rejects drift.
- Provider identity, model-instance identity, and ordered role routes are separate. Duplicate API model names across providers are supported.
- Both OpenAI protocols support tool roundtrips, cancellation, usage, safe errors, model discovery, and explicit embedding calls.
- Independent participation and learning switches; no proactive private messaging.
- Administration bearer authentication, no secret values in configuration responses, bounded technical events.

The initial foundation is not a completed autonomous bot. Runtime, SnowLuma integration, memory, learning, and media are delivered in subsequent coherent commits. No real OpenAI account or QQ/SnowLuma connection has been exercised; protocol tests use local fixtures.

## Development

The server schemas in `src/schema.ts` and route definitions in `src/api.ts` are the contract source of truth. After API changes run `npm run contract`, then `npm run check` and `npm run build`. Do not edit generated files. See [model protocols](docs/model-protocols.md) and [architecture boundaries](docs/architecture.md).

Secrets are server environment references. Configuration changes apply to new turns. Temporary chats, pending sends, attention, and simulation sessions are never restored after restart. Recall tombstones are durable provenance metadata, not an outgoing queue.
