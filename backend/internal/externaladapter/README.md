# External V2 signed HTTP adapter

An installed, explicitly listed `.v2.json` package can supply one adapter
without being compiled into HumanEngine or loading Node. The manifest puts
`"adapter"` in `capabilities` and includes
`"adapter": {"id": "actual-adapter-id", "wire": "signed-json-http-1"}`.
The package ID owns saved configuration and credentials. The actual adapter
ID owns runtime routing and must not be in use by any other installation.

The management form provides `listen` (host:port), `actionUrl`, `accountId`,
`botId`, and a write-only `key`. An omitted or blank key retains the stored
value; `clearKey` clears it. Configure the peer with the same key. Native code
owns the endpoint, credential, canonical scene/message/sender IDs, and policy
and authority work. The WASI guest never receives the key or an IO handle.

All wire requests use exact UTF-8 JSON bytes. The
`X-HumanEngine-Signature` value is lowercase hex HMAC-SHA256 with the stored
key over the ASCII purpose followed by NUL and the exact body. The three
purposes are `event` for POST `/event`, `action` for the configured outbound
POST, and `receipt` for the peer's outbound response to that POST. An
unconfirmed action is unknown and is not retried. Use a protected listener
or a TLS terminator for remote peers; the signature does not hide contents.

The existing V2 memory ABI wraps each operation in
`{"operation": "...", "input": ...}` (no input for `adapterProtocol`).

- `adapterProtocol`: returns `{"wire":"signed-json-http-1"}`.
- `adapterIngress`: translates a peer's raw JSON into an `event` projection
  with `type` (`message`, `recall`, or `ignore`), `peer` (`kind`, `id`),
  and platform `message`. A text message also requires `sender`, `text`,
  and RFC3339 `sentAt`, with optional platform `replyTo`. The host captures
  receive time and chooses live origin.
- `adapterSend`: receives the platform `peer`, runtime `parts`, and, when
  present, the platform `replyTo`; returns peer-specific action JSON.
- `adapterReceipt`: translates a valid signed successful HTTP response into
  `{"message":"platform-message-id"}`; any ambiguous outcome remains unknown.

This is the first external adapter wire profile: text ingress, guarded physical
send and scope-bound recall. It does not provide remote history, native media,
WebSocket, OneBot/SnowLuma compatibility, or product cutover. The Letterbox
fixture is independent test-only code, not a distributed plugin.
