# SnowLuma / OneBot connection

The adapter is independently implemented against OneBot's wire protocol and
SnowLuma's documented actions. No SnowLuma implementation code is vendored or
copied. The shared transport is `OneBotAdapter`; optional action names and
capability declarations are supplied by `OneBotProfile`. This release supplies
`SNOWLUMA_PROFILE` and a conservative `ONEBOT_V11_PROFILE`, not a NapCat plugin.

## Setup

1. In the account's SnowLuma network settings, enable a **Universal forward
   WebSocket server** and **array** message format. The default documented
   address is `ws://127.0.0.1:3001/`; use your actual configured address.
2. Configure an access token in SnowLuma and place the same token in a server
   environment variable. In HumanEngine, save only that variable's name as
   `credentialEnv`, never a token in the URL. Use `wss:` on untrusted networks.
3. Supply the exact QQ account ID. HumanEngine calls `get_login_info` before
   allowing actions and rejects an account mismatch. Events with a different
   `self_id` are discarded even on an established connection.
4. Enable the desired scenes in HumanEngine. Receiving an event does not itself
   authorize the runtime to participate or learn.

The adapter sends `Authorization: Bearer` in the upgrade request. URL credentials,
query strings, and redirects are rejected. Transport errors are fixed diagnostic
codes without token, URL, upstream wording, or message body.

## Public API

```ts
const adapter = new OneBotAdapter({
  id: 'snowluma-main',
  accountId: '123456789',
  url: 'ws://127.0.0.1:3001/',
  credentialEnv: 'SNOWLUMA_TOKEN',
}, {
  onMessage(message, scene) { /* Route through the core. */ },
  onRecall(event, scene) { /* Persist tombstone and invalidate dependencies. */ },
  isRecalled(sceneId, messageId) { return store.isRecalled(sceneId, messageId) },
})
await adapter.connect()
```

- `scene(kind, platformSceneId)` creates the stable, connection/account-scoped
  scene reference. `messageId(scene, platformId)` and `userId(platformId)` provide
  matching opaque IDs. Never construct neighboring message IDs arithmetically.
- `send(scene, segment, signal?)` sends one validated core segment, preserving
  text, mentions, and a quote. It returns `sent`, `failed`, `unknown`, or
  `cancelled`. Only a valid successful receipt supplies a normalized message ID.
- `getMessage(scene, id, signal?)` returns scoped history or an explicit reason.
- `history(scene, {limit, cursor?}, signal?)` returns a bounded older history
  window, an opaque continuation anchor when available, and honest coverage.
- `dispose()` rejects pending actions, terminates the socket, and cancels
  reconnection. There is no persisted connection/send queue.

Only the core should hold this adapter and call `send`; plugins use the core
send service. The adapter has no participation, learning, or role policy of its
own. Real chat preview is read-only at the core/API layer.

## History and recalls

The SnowLuma profile enables `get_group_msg_history` and
`get_friend_msg_history`. Both use an actual previous `message_id` with
`reverse_order: true`, whose window includes its anchor; HumanEngine removes the
repeated anchor, never increments/decrements IDs, and preserves negative IDs.
These are **SnowLuma extensions**, not promises of generic OneBot support. A
bounded result is always marked partial: an empty page cannot prove complete
historic coverage. Native by-user and arbitrary around-message lookup are not
claimed. Callers may implement bounded scans with explicit incomplete coverage.

`get_msg` and history can omit `self_id` in their response records. The established
connection is pinned to an account, while every returned record is independently
checked against the requested scene. A mismatch fails closed. History is always
`origin: history` and never emits a live message callback.

`group_recall` uses `group_id` plus `message_id`; `friend_recall` uses the peer's
`user_id` plus `message_id`. Both require the matching account `self_id`. The
adapter records a process-local tombstone before notifying the core and consults
`isRecalled` to apply the core's persistent tombstones. A late history/get result
cannot resurrect content after a recall; live replays have empty parts and a
recalled status. The core must also invalidate in-flight generation and pending
learning, remove affected evidence, and check send eligibility after every await.
An account being offline or missing an upstream recall notice is not evidence
that no recall occurred.

## Delivery and media limits

WebSocket `echo` correlates responses; it is **not** a platform idempotency key.
A timeout, disconnect, or cancellation after transmission makes a send unknown.
The adapter never retries it. Reconnection re-establishes reception and validates
the account again, but does not replay actions or old history as new input.

Received image/audio/video parts preserve remote references without downloading
or interpreting them. By default such references can only be sent in their source
scene. The core may inject `resolveMedia(assetId, scene)` for an already authorized
shared asset; it owns privacy, local file, URL, and MIME validation. Unsupported
segments are explicit, not silently converted into plausible text. Configure
array format; CQ-string parsing is intentionally not guessed.

GIF/WebP playback on recipient clients remains unverified. This adapter does not
claim animation transcoding, album upload, arbitrary file send, native per-user
history, or temporary private sessions originating from groups. Group-temporary
private events are rejected rather than accidentally sent as ordinary friend
messages. Ordinary passive friend/private conversations are supported; the core
must disallow proactive private messages.

## Evidence and tests

Reviewed official action contracts and account/recall envelopes at SnowLuma
commit `3bd74acb60e45fed7fa0451be11c54905dee0670` on 2026-10-25. The local WebSocket
fixtures cover auth, account/scene isolation, text/mention/quote conversion,
private direction, recall/history races, persisted recall consultation, send
receipts, unknown delivery, cancellation, reconnect/dispose, and generic-profile
capability limits. **No real QQ account or live SnowLuma instance was tested.**

- [SnowLuma API and authentication](https://snowluma.github.io/en/docs/api)
- [Group history](https://snowluma.github.io/en/docs/api/extended/get_group_msg_history)
- [Friend history](https://snowluma.github.io/en/docs/api/extended/get_friend_msg_history)
- [Single-message lookup](https://snowluma.github.io/en/docs/api/message/get_msg)
- [OneBot recall notices](https://github.com/botuniverse/onebot-11/blob/master/event/notice.md)
- [SnowLuma notice envelopes](https://github.com/SnowLuma/SnowLuma/blob/3bd74acb60e45fed7fa0451be11c54905dee0670/packages/onebot/src/event-converter/to-notice.ts)
