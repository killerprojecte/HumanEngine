# Native image compatibility module

This source-only checkpoint implements the source image-upload pixel pipeline
with the exact official libvips artifact used by the pinned Sharp dependency.
It does not include a shared object, enable production provider composition or
claim that the complete native binary redistribution package is finished.
The current native ABI/artifact target is Linux x86-64 with glibc.

Actual TypeScript fixtures cover 52 byte/error cases, including real UltraHDR
gain-map JPEG, color profiles and 16-bit conversion, orientation, animation,
warning decode, resize/search and cancellation. All passed locally under race;
vet and valid-pixel cases below/at/above the existing 40M source policy passed.
The standalone probe also ran all 52 cases from a separate directory with an
empty environment and no Node/Go on PATH. Local ptrace denial prevented tracing;
that run is a functional proof, not a file-access trace proof.

`make native-image-check` regenerates actual-source fixtures, tests the native
module and requires a file-traced standalone proof. CI installs strace explicitly.
It obtains the exact library from the existing lockfile-pinned npm installation
for testing only. The Go runtime does not start Node or load the Sharp addon.
VIPS_CONCURRENCY=1 in this test target controls shared validation resources; it
is not a new production concurrency limit. Production cache/concurrency behavior
follows the source implementation.

Complete library copyright/source materials and recipient rebuilding instructions
must accompany a future binary distribution. The included vendor notice is an
inventory, not a substitute for that package. Acquisition scripts and pinned
upstream build sources are included; private download observations and partial
archives are not. No new production resource caps are introduced.
