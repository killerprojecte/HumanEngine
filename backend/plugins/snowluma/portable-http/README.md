# External SnowLuma HTTP preview

This is an opt-in proof for a **disposable installation**, not a replacement
for the bundled SnowLuma adapter. Do not select it on an existing installation:
it deliberately uses the same plugin/adapter ID and credential names to exercise
canonical identity, but does not yet implement WebSocket, legacy environment
credentials, remote history, media, or full OneBot message-part parity.

From `backend`, use the pinned Go toolchain and run:

```sh
make wasm-snowluma-http-preview
```

This generates `snowluma-http.v2.json` and `snowluma-http.wasm` under
`plugins/snowluma/dist/portable-http/`. Keep both files together; the generated
manifest identifies the exact artifact bytes. Neither output belongs in source
Git. Select the generated manifest in the unbundled preview using the existing
`HUMANENGINE_PLUGINS` entry list and a disposable plugin directory and database.

The package owns login-response checking and OneBot event, action and ACK
projection. HumanEngine supplies the versioned OneBot HTTP envelope transport,
scoped credentials, runtime registrations and durable message/recall ownership.
No SnowLuma implementation package is linked into the unbundled Go host.
