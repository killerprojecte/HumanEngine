# Trusted local plugins

Only install code you trust with the host process and server credentials. This SDK is not an untrusted-code sandbox.

Put an ECMAScript module in `plugins/`, export the plugin object as default, and set `HUMANENGINE_PLUGINS=example.mjs` (comma-separated entries are supported). The module is registered at server startup. Configure it on the Plugins page before enabling; saved enable state is restored on startup. Restart after adding/removing module files. Configuration changes reload only the affected enabled plugin.

`plugins/example.mjs` and `public/plugins/example/status.html` demonstrate both schema forms and a custom administration page. Add public page assets before the production build. Custom pages use `/api/plugins/{id}/actions/{action}` with the normal administration bearer token; neither a page nor API responses should receive provider keys or database paths.

Supported configuration vocabulary: objects, strings, booleans, numbers/integers, arrays, required fields, defaults, descriptions, groups, enums, basic length/range constraints, and recursively redacted `secret` fields. Use environment-variable references for provider/platform credentials. Blank redacted secrets preserve existing values; explicitly submit a replacement to rotate one.

Hooks run in declared priority order and receive linked turn/plugin cancellation. Cancel is terminal. Disable removes new registrations, aborts pending work, disposes resources, and prevents late asynchronous setup from re-enabling a plugin. All platform sends remain core-owned; plugins can shape/cancel them via send hooks or request a send through an effect-declared tool invocation. Neither route exposes a raw adapter-send bypass.

Readonly events use `context.on(type, listener)` for `message.received`, `message.sent`, and `runtime.error`. Payloads have `version: 1`, are deeply frozen independent snapshots, and cannot alter the core. Listeners run asynchronously and failures are isolated. A recall received before queued delivery strips the withdrawn message body. Simulation never emits these real-platform events. Disabling unregisters listeners and prevents queued delivery to that plugin; callbacks already running must honor the plugin signal and must not retain raw chat unnecessarily.

## Model protocol extensions

The default protocols remain **OpenAI Chat Completions** and **OpenAI Responses**.
A trusted local plugin can add another protocol; no Anthropic implementation is
included. The protocol registry belongs to one engine, not the whole process.

Declare `model-protocol` and register a short local name. The public protocol ID
becomes `<plugin-id>:<name>`; built-in names cannot be overridden. Registration is
staged until plugin setup succeeds. This registration example binds your own
local implementations, not a bundled third-party transport:

```js
import { modelProtocol } from './my-model-protocol.mjs'
import { createChatTransport } from './my-chat-transport.mjs'

export default {
  manifest: {
    id: 'local-integration',
    name: 'Local integration',
    version: '1.0.0',
    description: 'Trusted local protocol and transport integration',
    configSchema: { type: 'object', properties: {}, additionalProperties: false },
    capabilities: ['model-protocol', 'adapter'],
  },
  setup(context) {
    context.registerModelProtocol('vendor', 'Local vendor protocol', modelProtocol)
    context.registerAdapter('chat', createChatTransport)
  },
}
```

`ModelProtocol` is exported from `src/model-protocols.ts`:

- `complete(context, messages, tools, continuation?)` is required. Return
  `{ text, toolCalls, usage, continuation? }`. Tool calls use normalized dotted
  names and object arguments, not provider-specific wire JSON strings.
- Optional `embeddings(context, inputs)` returns `{ vectors, usage }`.
- Optional `listModels(context)` returns an array of API model-name strings.
- Context contains `provider`, `model` (absent for discovery), `signal`, and the
  actual server-only `credential`. Usage has nonnegative integer `inputTokens`,
  `outputTokens`, and `totalTokens`; use `known: false` with numeric placeholders
  when counters are unavailable. Unknown usage is not evidence of free usage.

Core scheduling, request deadlines, configured instance selection, normalized
response validation, usage accounting, and sanitized diagnostics wrap custom
calls. Do not implement independent fallback or execute returned tools inside a
protocol. Return complete results only, never execute streamed partial tool
arguments. Core owns subsequent tool execution and scene/simulation checks.

Opaque continuation is attached privately to the exact returned assistant object
and isolated by model instance and registration generation. It is not placed in
messages, logs, APIs, or visible diagnostics; it is not restored after restart.
The same API model name does not imply the same instance or continuation. Model
requests and tool definitions are copied before being passed to custom code.

Disabling/reloading removes the registration, aborts queued/in-flight work, and
rejects late results. Disabling an active call is terminal, not a reason to start
another fallback instance. Honor the linked signal in network calls. Core can
stop awaiting an uncooperative plugin, but cannot forcibly stop synchronous
JavaScript, remote provider billing, or arbitrary host side effects.

`GET /api/model-protocols` exposes only IDs, labels, and supported operations.
Provider forms retain configured but currently unavailable custom protocol IDs;
they do not silently switch to OpenAI. Credentials remain environment references
in browser configuration and are resolved only on the server. Never put keys in
labels, continuation visible output, exceptions, URLs, or custom-page responses.

## Platform adapter extensions

Declare `adapter` and call `registerAdapter(name, factory)`. The adapter ID is
`<plugin-id>:<name>` (maximum 64 characters). `AdapterFactory` and
`AdapterContext` are exported from `src/plugin-adapters.ts`. The factory constructs
and returns a `RuntimeAdapter` with optional async `start()`:

```ts
// Contract shape for your local transport implementation; not a bundled platform.
function createChatTransport(context: AdapterContext): RuntimeAdapter & {
  start?(): Promise<void>
}
```

The factory should only construct resources. Begin listening/connecting in
`start()`, after installation; early ingress during factory construction is not
accepted. Startup is bounded and participates in the plugin lifecycle. `dispose()`
must synchronously close subscriptions/sockets where possible and be safe to call
once after partially completed startup. Network operations must honor the linked
signal. Report connection changes with `context.status(...)`; do not report a
connection as verified merely because the factory returned.

Use the helpers for every ingress, lookup, receipt, and recall:

```js
const scene = context.scene(accountId, 'group', platformGroupId)
const id = context.messageId(scene, platformMessageId)
context.receive(
  {
    id,
    sceneId: scene.id,
    senderId: platformSenderId,
    sentAt: platformTimestamp,
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: platformText }],
  },
  scene,
  isAuthenticatedBotAuthor,
)
// The recall must use the same verified account and scene as the original event.
context.recall(scene, context.messageId(scene, recalledPlatformMessageId))
```

IDs are opaque and account/adapter/scene-scoped. Never invent prefixes, compare
IDs arithmetically, or reuse a message ID across scenes. Hashed message IDs cannot
be decoded into native IDs: your transport must retain the appropriate native-ID
mapping or return honest unavailable/partial coverage when a lookup is impossible.
The boolean bot-author flag is mandatory: identify the authenticated bot's own
messages correctly, including events that arrive before a send receipt, so they
cannot trigger self-learning or reply loops. New scenes are unconfigured and
passive until an administrator enables them.

The returned transport implements:

- `send(scene, segment, signal?)`: called **only by the core send pipeline**.
  Return `sent`, `failed`, `unknown`, or `cancelled`; canonicalize receipt IDs with
  `context.messageId`. A disconnected/timed-out request with uncertain delivery
  is `unknown`, never automatically retried. There is no `context.send` bypass.
- `getMessage(scene, id, signal?)` and `history(scene, { limit, cursor? }, signal?)`:
  return normalized `HistoryResult` with truthful `complete`, `partial`, or
  `unavailable` coverage. Use history origin, preserve opaque pagination cursors,
  and never turn history backfill into live ingress.
- `dispose()`: release transport resources without trying to replay pending sends.

Core validates scene/account namespaces and strips recalled/unavailable message
bodies; persistent recall tombstones prevent history backfill from resurrecting
known withdrawn sources. Use `context.isRecalled(scene, id)` as an additional
transport check. `context.media(assetId, scene)` resolves only core-authorized
local media for that scene; do not fetch arbitrary URLs or assume the entire
shared library is public. No image/animation capability is inferred from a
transport registration.

Simulation does not call real adapter sends/history or emit real-platform events.
Plugin tools remain subject to their declared effects: learning-candidate tools
are rejected, while send tools route through the local simulation send pipeline
instead of a real transport. A trusted plugin still has arbitrary host capabilities:
these guards are SDK contracts, not isolation from malicious installed code.
Adapter shutdown cancels its active scene work and queued learning; credentials
and native transport connections are not restored from a backup. SnowLuma remains
the initial built-in OneBot integration; this SDK does **not** add NapCat.

## Tool send capability and output contracts

A registered tool declares `effect: 'read' | 'learningCandidate' | 'send'`. Its
`PluginToolContext` is scoped to the original turn, with a frozen scene snapshot,
linked cancellation, and a `send(request)` capability. Only `effect: 'send'` may
use that capability. It cannot select a different scene or change the turn's
mode, and it goes through core segment validation, hooks, recall/participation
checks, deduplication, and receipt handling. Simulation receipts remain local.

```js
context.registerTool({
  name: 'local-integration.deliver',
  description: 'Deliver one approved expression through the core pipeline',
  effect: 'send',
  inputSchema: {
    type: 'object',
    properties: { text: { type: 'string', minLength: 1, maxLength: 12000 } },
    required: ['text'],
    additionalProperties: false,
  },
  outputSchema: { type: 'array' },
  async execute(input, turn) {
    return await turn.send({
      operationId: 'reply',
      segments: [{ parts: [{ type: 'text', text: input.text }] }],
    })
  },
})
```

Always await the send; do not retain the capability or start detached work after
the invocation ends. Keep operation IDs stable for the same expression within
one turn, and never automatically retry an unknown delivery outcome. A tool may
also declare `outputSchema` using the same supported JSON-schema subset as its
input. Results must be JSON-compatible, fit within 1 MiB, and pass the declared
output schema; a JSON clone is returned rather than a live plugin-owned object.
These contracts do not grant tools permission to write source code, fixed
prompts, or global person memory.

Use `senderId: 'bot'` for own historical messages and `userId: 'bot'` in authenticated bot mentions. Live `receive` still requires an explicit `isBot` boolean. This reserved identity avoids learning from the bot itself or mistaking another member's mention for an address to the bot.
