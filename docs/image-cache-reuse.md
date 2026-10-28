# Image read and interpretation reuse

The image pipeline distinguishes four operations: a verified local library lookup, an
in-memory byte read, an adapter read, and a vision-model request. A log saying that
image understanding is being resolved does not imply a new download.

- Scene/account/message-scoped, hashed opaque-reference mappings are written only after
  actual image bytes match the stored content hash and an authorized source.
  They contain no raw platform URLs, credentials, or media tokens.
- Known (not included for sending) memes can supply their stored interpretation
  and verified local original. This does not grant send/search eligibility.
- Current library semantics take precedence over temporary descriptions. A
  valid temporary description also avoids reading bytes, even with library
  recognition enabled. Administrative changes and source recall remain checked.
- Background intake, model reads, and display reads share a bounded byte cache
  and pending reads. Cache hits still require exact scene/message/part authority.
- A new message source must be read once before content equality is established,
  even if its URL has the same spelling. Repeated turns on the same historical
  message can reuse the local mapping across restarts. Signed URL parameters and generic filenames are not
  stripped to guess that two resources are identical.
- A model-stage timeout remains a model-stage timeout. This change does not
  increase configured budgets or invent descriptions for a failed image. Other
  successful image observations remain available to the reply pipeline.

Each turn selects at most four authorized message/part sources, preserving explicit
question/reply priority and reporting excess sources as omitted. Actual byte hashes
can deduplicate successful model descriptions after those bounded source reads.
A failed source is attempted at most once within the same turn.

## Primary-source comparison

Reviewed MaiBot at commit
[`6bcb2e068cd98793a132cb8cc4ebf3cc00cb8fc3`](https://github.com/Mai-with-u/MaiBot/tree/6bcb2e068cd98793a132cb8cc4ebf3cc00cb8fc3):

- [`image_manager.py`](https://github.com/Mai-with-u/MaiBot/blob/6bcb2e068cd98793a132cb8cc4ebf3cc00cb8fc3/src/chat/image_system/image_manager.py)
  persists content-addressed image files and descriptions, and reads completed
  descriptions without another model request.
- [`emoji_manager.py`](https://github.com/Mai-with-u/MaiBot/blob/6bcb2e068cd98793a132cb8cc4ebf3cc00cb8fc3/src/emoji_system/emoji_manager.py)
  separates cached emoji descriptions from registration in its sendable pool.
- [`emoji_image_cache.py`](https://github.com/Mai-with-u/MaiBot/blob/6bcb2e068cd98793a132cb8cc4ebf3cc00cb8fc3/src/maisaka/visual/emoji_image_cache.py)
  bounds converted model-image caching by bytes and entries and coalesces work.

HumanEngine implements these separations within its own scene/source permissions;
no upstream implementation is copied, and no inference is made about real model
quality from synthetic test responses.

Also reviewed [SnowLuma inbound codec at ab751ae](https://github.com/Mai-with-u/MaiBot-SnowLuma-Adapter/blob/ab751ae5c11f501e0158e33cacc321b33e34ef0c/codecs/inbound/message_codec.py): its content identity is SHA256 of actual binary data, not a signed URL. HumanEngine does not inherit platform-supplied local-file access.
