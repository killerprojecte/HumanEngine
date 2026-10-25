# Trusted local plugins

Only install code you trust with the host process and server credentials. This SDK is not an untrusted-code sandbox.

Put an ECMAScript module in `plugins/`, export the plugin object as default, and set `HUMANENGINE_PLUGINS=example.mjs` (comma-separated entries are supported). The module is registered at server startup. Configure it on the Plugins page before enabling; saved enable state is restored on startup. Restart after adding/removing module files. Configuration changes reload only the affected enabled plugin.

`plugins/example.mjs` and `public/plugins/example/status.html` demonstrate both schema forms and a custom administration page. Add public page assets before the production build. Custom pages use `/api/plugins/{id}/actions/{action}` with the normal administration bearer token; neither a page nor API responses should receive provider keys or database paths.

Supported configuration vocabulary: objects, strings, booleans, numbers/integers, arrays, required fields, defaults, descriptions, groups, enums, basic length/range constraints, and recursively redacted `secret` fields. Use environment-variable references for provider/platform credentials. Blank redacted secrets preserve existing values; explicitly submit a replacement to rotate one.

Hooks run in declared priority order and receive linked turn/plugin cancellation. Cancel is terminal. Disable removes new registrations, aborts pending work, disposes resources, and prevents late asynchronous setup from re-enabling a plugin. All platform sends remain core-owned; plugins can shape/cancel them via send hooks. Arbitrary direct plugin sending is not provided as a separate bypass.

Readonly events use `context.on(type, listener)` for `message.received`, `message.sent`, and `runtime.error`. Payloads have `version: 1`, are deeply frozen independent snapshots, and cannot alter the core. Listeners run asynchronously and failures are isolated. A recall received before queued delivery strips the withdrawn message body. Simulation never emits these real-platform events. Disabling unregisters listeners and prevents queued delivery to that plugin; callbacks already running must honor the plugin signal and must not retain raw chat unnecessarily.
