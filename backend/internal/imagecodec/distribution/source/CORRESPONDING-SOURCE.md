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

## Observed Linux x64 release dependency resolution

The upstream [x64 build job](https://github.com/lovell/sharp-libvips/actions/runs/36315646015/job/108609814156)
and [npm publish job](https://github.com/lovell/sharp-libvips/actions/runs/36315646015/job/108616203584)
are from the same run at `ebb95f8`. The publish job logged npm archive SHA-1
`a0de3840ae386476eb4045d6d2b056a8062c548b` for the x64 glibc package and
[transparency log entry 2974712411](https://search.sigstore.dev/?logIndex=2974712411).

The checked-in original lock matches the
[GNOME release commit lock](https://github.com/GNOME/librsvg/blob/073326620abe168bb8be07d9c52595a5d19d1ecb/Cargo.lock)
(blob `c78cf09e12e2a998a5ffdb4528772f95b414d73c`). Local SHA-256 receipts:

- `librsvg-2.63.2-Cargo.lock`: `e79e2a2735a0b5b151bb4133becd3008223fabedd34a8b787a872edab9854941`.
- `rust-trusted-lock.json`: `7d4146c452a3974f09591517c0f43813ad417402a5bd308e4603c60b84e18f2a`.

The x64 build logged zero new locks and four removals: `color_quant 1.1.0`,
`gif 0.14.2`, `image-webp 0.2.4`, and `weezl 0.1.12`. To reproduce the
inferred inventory, compare the original 357 registry name/version/checksum
entries to the trusted manifest, then subtract those exact pairs. For inferred
adjacency, resolve original dependency references by name and version before
pruning: only `image 0.25.10` loses edges to the first three removed packages.
`weezl 0.2.1` remains. The resulting 353 registry entries contain all 162
compiled and 105 downloaded registry pairs logged for librsvg. These are
observations of that build, not all features or proof of linked contents.
Neither the actual regenerated lockfile bytes nor a Cargo re-resolution was
obtained; do not use this inferred inventory as a replacement lockfile.

The build recorded Rocky `8-ubi-init` digest
`sha256:ae0813ccb1bb83f4e0220acdd1539f52831517de334368e960824db7e37ee210`,
Rust nightly 2026-09-27 (`1.101.0-nightly`, `75a75c3e0`), cargo-c
`0.10.25+cargo-0.99.0`, Meson `1.12.1`, and Ninja `1.13.2`. These identify
observed upstream build inputs; they are not dependencies of the Go runtime
or a complete toolchain replay recipe.

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
5. Pin and verify the build environment for replay. The digest and versions above
   were observed, but the upstream recipe still has mutable tooling selections
   and unpinned dnf inputs. It is not currently a bit-reproducible binary recipe. Do not promise the rebuilt SHA will equal the bundled SHA.
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
