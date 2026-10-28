# SnowLuma image observations before participation

## Reference checked

The actual MaiBot SnowLuma adapter was inspected at commit
[`ab751ae5c11f501e0158e33cacc321b33e34ef0c`](https://github.com/Mai-with-u/MaiBot-SnowLuma-Adapter/tree/ab751ae5c11f501e0158e33cacc321b33e34ef0c).
Its `codecs/inbound/message_codec.py` resolves actual binary media before handing
image/emoji segments to its host: inline data, file/URL references, and a
`get_image` fallback. It distinguishes image subtype/summary hints and represents
native QQ faces with descriptive text. Its manifest declares GPL-v3.0-or-later;
no source code or face-description table was copied into HumanEngine.

This is an independent implementation. Unlike the referenced adapter,
HumanEngine never reads a path supplied by a remote QQ event or action response.
Remote downloads retain the existing QQ-host, public-address, pinned-DNS,
no-redirect, byte-limit and cancellation protections. Adapter credentials are
used for OneBot actions, never forwarded to image URLs.

## Confirmed defect

Previously `participation.decide` ran before the image-description hook. A
legitimate `silent` decision therefore prevented vision from running at all.
The initial planner saw only media references, even when a vision model was
configured. Earlier tests forced the participation reply branch and missed this
ordering error. Renaming the UI's media placeholder would not repair it.

Current topic images and directly referenced source images now receive bounded,
scene-scoped visual observations before the initial participation decision.
The same attributed, untrusted observations feed later planning/dialogue without
a second vision request. Silence remains valid after inspecting the content.
An unavailable vision route or failed intake yields an explicit unavailable
observation; it does not fabricate a description or force a reply.

## Supported representations and limits

- Array and CQ image references; inline base64 takes precedence over stale URLs.
- File tokens resolve through authenticated `get_image`; a preserved token can
  refresh an expired URL once.
- Image-like `mface`, sticker and emoji segments use the same safe intake when
  they contain a usable reference. Platform summaries/subtypes remain explicitly
  untrusted metadata. Metadata-only stickers are reported unavailable, not
  downloaded from invented URLs.
- Native QQ `face` IDs and provided labels remain metadata; unknown meanings are
  explicit. No copyrighted upstream face lookup table is vendored.
- PNG, JPEG, GIF and WebP bytes are validated and converted to bounded PNG input.
  Only the first frame is supplied; the model and returned observation explicitly
  disclose that motion and later animation frames are unavailable.
- At most four images per context, an expiring process-local cache, recall and
  scene fences, and cancellation checks remain in force. Learning can be disabled.

The message's `image/unknown` media type is ingress metadata, not a claim about
whether vision succeeded. Chat UI attachment rendering and model image context
are distinct surfaces; these changes repair the latter.
