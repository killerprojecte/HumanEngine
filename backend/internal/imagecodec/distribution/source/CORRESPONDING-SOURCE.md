# Exact source acquisition and distribution status

The official npm publication for @img/sharp-libvips-linux-x64 1.3.4 identifies
packaging commit ebb95f8add54eee8bed840e3fb587e4cbec857d7. The extracted contents of its complete 24,490-byte
source archive are included; the original archive hash is recorded in the acquisition material. All 28
versions.properties entries match the binary's shipped versions.json exactly.
The exact build/posix.sh, Linux x64 Dockerfile, meson/CMake toolchains, inline
patches and full packaging source are included. The upstream third-party notice
is only a license inventory, not every library's full copyright notice.

## Current material status

All 33 active native source/patch inputs were retrieved from the official recipe
and their observed hashes are recorded in native-source-lock.json. The source
archives are not included in this code checkpoint. Full legal texts were
collected from all 28 source archives, but the complete delivery bundle and Rust
vendor material remain unfinished. These observations are not a bit-reproducible
binary-build claim.

## Still required before distributing the combined native library

1. Retrieve and archive every source and patch URL in acquisition-manifest.json.
   Record SHA-256 and verify upstream checksums/signatures or immutable commits.
   Include the relevant libvips embedded sources (including libnsgif), and all
   linked library copyright/license files. Keep source trees before and after
   the exact recipe's patches and sed transformations.
2. Provide source and rebuilding instructions for the LGPL components: libvips,
   glib, libexif, libheif, fribidi and librsvg. The proxy-libintl source appears in
   the common recipe but is conditional on musl/macOS, not this glibc artifact.
   Include Cairo source/material under its selected MPL terms. Providing all
   statically linked dependency source archives is the conservative bundle.
3. Include librsvg's Rust dependency sources and Cargo.lock after the recipe's
   cargo update --workspace. Capture cargo vendor output and all Rust crate
   notices. The inventory alone is insufficient for those dependencies.
4. Preserve exact patched mozjpeg and libvips sources. Recipes reference pinned
   patch commits/gists, except libultrahdr PR383's patch URL can change. Archive
   and hash that patch before claiming reproducible source selection.
5. Pin the build container digest and tool versions: upstream's Rocky image tag,
   dnf updates, Rust nightly, cargo-c, meson and ninja are not immutable. The
   recipe is exact for this package commit, but not currently a bit-reproducible
   binary recipe. Do not promise the rebuilt SHA will equal the bundled SHA.
6. Supply buildable Go/C bridge sources and a documented route to substitute a
   rebuilt library hash and relink the application; permit debugging modified
   LGPL components. The default hash check must not be represented as a ban on
   recipients rebuilding/replacing the library.
7. Publish this source material alongside the exact binary, at a stable verified
   download location, or deliver the complete source archive with the binary.
   Do not rely on this document as a written source offer or promise hosting that
   does not exist. Do not claim a few external URLs satisfy the entire obligation.

The acquired native inputs and their observed hashes are not the complete
redistribution bundle. Rust material, full notice reconciliation, rebuilding
instructions and actual artifact delivery remain unfinished. Including the
LGPL/GPL texts alone is not a claim that a binary release is complete.
