# Minimal libvips comparison (not a replacement)

The tested dependency remains the exact official libvips shared object. A minimal
build must have a different hash and cannot replace it until the entire actual
source differential, real 40M-pixel boundaries, malformed input, color/ICC,
orientation, animation and concurrency/lifetime cases pass byte-for-byte.

## Potential savings, based on exact upstream source/configuration

The full package declares 28 direct source versions (proxy-libintl is conditional
on musl/macOS). A conservative minimal Linux/glibc configuration retains 12 direct
sources: libvips, glib, libffi, expat, zlib-ng, libexif, lcms, patched mozjpeg,
libpng, libwebp, highway and libultrahdr. Keep libnsgif embedded in libvips and
GLib's gvdb fallback source. Core Meson requires glib/gio/gobject and expat.

Keep Highway and the exact codec versions/patches/optimization flags: changing
SIMD, arithmetic or JPEG implementations can invalidate byte parity. Keep LCMS,
profiles and libexif for ICC/CMYK/P3/orientation behavior. GIF decoding uses the
embedded libnsgif; cgif and libimagequant serve output paths not used by this
source's PNG/JPEG encoding settings and can be disabled for comparison.

Crucially, libultrahdr must remain: Sharp maps Uhdr loaders to JPEG. A genuine
small generated JPEG gain-map sample passes metadata/stats and the source's
compressImage passthrough/firstFrame/resize paths. Source preload reduction
explicitly switches to jpegload_buffer; initial decoding auto-detects UHDR.
Calling this a four-format codec is not permission to drop JPEG subtypes.

Potentially removed: aom/libheif, cairo/pixman, librsvg and its Rust crates,
pango/fribidi/harfbuzz/fontconfig/freetype, libarchive/libxml2, TIFF, cgif and
imagequant. This removes at least 62,137,878 bytes of currently known compressed
source archive sizes, before unreported sizes and all Rust crates. Retained
sources currently account for at least 44,725,871 known bytes plus unknown sizes.
These are HEAD-derived lower bounds, not predicted binary sizes. Add 24,716
bytes for the pinned gvdb source. The 357 original-lock Rust archives all replied
HTTP 200 and total 38,675,419 compressed bytes, also avoidable in a future minimal
build. This does not remove obligations of the currently bundled full library.

The original librsvg Cargo.lock contains 357 registry crates (366 total packages).
That conservative source superset is not the exact post-patch compiled closure.
Removing librsvg avoids the Rust source/vendor/notice work for a new minimal
binary, but it cannot erase source/license obligations of the current full .so.

## Executable preparation

tools/configure_minimal_vips.sh prints the exact experimental Meson option plan
by default. --execute only configures an already-patched source and staged prefix;
it performs compiler probes and therefore needs a coordinated build window.
It installs/downloads nothing and does not change the production library hash.

To actually attempt this alternative, first acquire and lock retained sources,
patches and gvdb, pin the toolchain/container, reproduce upstream dependency
build flags, then build only in a separately approved single-job window. No
minimal library has been built or benchmarked. Exact build time, peak RAM and
binary size cannot be known from this static analysis.
