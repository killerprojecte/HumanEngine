# Image upload compression

Native `openai-chat` and `openai-responses` providers accept optional
`imageUploadLimits` in provider configuration (also exposed in the provider editor):

```json
{ "maxImageBytes": 10485760, "maxWidth": 1536, "maxHeight": 1536, "maxRequestBytes": 20000000 }
```

These are deployment-specific settings, **not universal provider limits**. Set them
to the actual recipient/proxy requirements. The first three fallback values retain
HumanEngine's existing 10 MiB / 1536-pixel local vision policy. There is no guessed
whole-request cap when `maxRequestBytes` is omitted. Custom protocol plugins own
encoding/transport and must enforce their own recipient admission.

Source acquisition has a separate 64 MiB safety ceiling and decoding is capped at
40 million pixels. Sources larger than the former 10 MiB cutoff can now reach
compression. Originals remain unchanged in adapter/storage/display paths.
Acceptable static images retain exact bytes; transformations strip metadata,
auto-orient, preserve aspect ratio, and never upscale. Lossless PNG is attempted
first; opaque images then use an adaptive JPEG quality search. If necessary,
dimensions shrink repeatedly down to 1 × 1. Alpha remains lossless rather than
being silently flattened. Invalid/unsupported input, the decode safety ceiling,
and budgets smaller than even a minimal image/envelope remain explicit failures.

The existing vision pathway intentionally supplies only the first animation frame
and tells the vision model that later frames/motion are unavailable. Other native
image callers retain acceptable animations; required animation transformations
fail explicitly instead of silently discarding frames. Remote image URLs are not
fetched from the model transport, avoiding a new SSRF path.

Request admission accounts for the exact UTF-8 serialized native JSON including
all images, data-URL prefixes, base64 padding, text, and tools. For an aggregate
limit, images share the available bytes and are compressed again; the final wire
body is checked before sending. A native HTTP 413 on inline-image requests causes
adaptive retries, each halving the previous actual request size under
the original timeout/cancellation and normal budget admission. No arbitrary error
is treated as evidence that an image is too large, and provider response bodies
are not exposed in errors. An unknown stricter provider can still reject all
feasible reductions; retries stop at the irreducible image/envelope minimum or original deadline. Configure its real limits to fit before the first request.
