# External SnowLuma HTTP preview

This is an opt-in proof for a **disposable installation**, not a replacement
for the bundled SnowLuma adapter. Do not select it on an existing installation:
it deliberately uses the same plugin/adapter ID and credential names to exercise
canonical identity, but does not yet implement WebSocket, legacy environment
credentials or full OneBot message-part parity.

Remote group and friend history use the existing bounded history window.
Single-message lookups validate the exact requested conversation, so an equal
platform message ID in another conversation cannot supply a history image.
History reads do not ingest or replay messages.

Outgoing image parts can use scene-visible originals selected from the managed
sticker library. The guest chooses the wire segment; the host resolves and
encodes the original, then rechecks it at transmission. Raw image paths and
bytes never cross into the guest.

Incoming text and still images keep their order. An image may be inline or
resolved from an opaque token with an authenticated OneBot `get_image` call.
This preview reads inline bytes from that response; it does not download a
returned URL or open a local path. The browser uses the host's authenticated
message media route and a recalled message no longer serves its image.

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
