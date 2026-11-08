# Native image codec release gates

This directory is a source-only candidate, not a complete binary release artifact. Do not
wire it into a production provider until the differential and packaging gates pass.

## Reproducible dependency

Obtain the exact npm artifact and integrity from manifest.json (official
@img/sharp-libvips-linux-x64 1.3.4). Extract only the pinned shared object into the
application's native/linux-amd64 directory. Verify SHA-256 before loading. The
library requires Linux x86-64 glibc >=2.28. No Node runtime, sharp addon, TS source,
headers or build tree is a runtime dependency. C ABI declarations are opaque public
API signatures, not private structure replicas. Preserve source for this bridge.

## Licensing gate

Ship full LGPL-3.0/GPL-3.0 license texts, all bundled-library notices, the exact
corresponding sources/build scripts for libvips and LGPL dependencies, and an
explicit explanation of replacing/rebuilding the dynamic library. The stock npm
README is a license inventory, not the complete license/source offer. The default
hash pin is a reproducibility check, not a restriction on recipient modifications:
provide buildable bridge sources and explain how to rebuild with a replacement
hash/library. Sharp algorithm adaptations require sharp's Apache-2.0 notice.
Do not distribute claiming LGPL compliance until this bundle is complete.

## Pixel/source proof

Run the actual src/image-upload.ts oracle generator and --check. It currently
covers all original twelve cases, animation preservation/refusal/first-frame,
truncated warning decode, JPEG search, GIF/WebP resizing, all EXIF orientations,
invalid JS-safe integer and cancellation. Run TestActualSourcePixels against the
pinned artifact. Exact encoded bytes, not image envelopes or dimensions, must
match. The current matrix includes profiled CMYK/P3, grayscale/16-bit, oversized
headers, concurrent/cached input checks and valid pixels below/at/above the
existing 40M policy. All 52 small cases and three large cases passed locally;
universal codec equivalence is not claimed.

The native stats pass includes decoded pixel stats, grayscale histogram entropy,
Laplacian deviation and dominant-color histogram evaluation. Full source error
parity still requires the differential gate.

## Distribution/source-free proof

Build a standalone Go probe accepting fixture JSON and a library path; copy only
that executable, fixture JSON and the verified shared library into a temporary
empty directory. Run with env -i, empty PATH and no NODE_PATH. Assert all encoded
output/error expectations; inspect ldd/readelf and process/file trace to prove no
Node, TS, sharp addon or source tree was opened. This must run, not merely exist as
a design, before source-free deployment is accepted. Do not call a test binary
running in a source checkout source-free proof.

## Cancellation

Match the source's explicit check locations: before admission, each scale pass,
and each JPEG quality trial. The source does not interrupt an in-flight sharp
await. Native work likewise finishes each libvips operation; no artificial
watchdog, dimension cap, retry bound or extra protective limit is introduced.
