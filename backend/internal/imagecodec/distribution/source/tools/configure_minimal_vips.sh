#!/usr/bin/env bash
# EXPERIMENTAL COMPARISON ONLY. This is not the tested production dependency.
# Requires the exact pre-patched vips source and staged, version-verified deps.
# No download/install occurs. Default is a plan; --execute invokes Meson/CMake
# compiler probes, so it also requires a coordinated build window.
set -euo pipefail
if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then echo "Usage: $0 PATCHED_VIPS_SOURCE STAGED_PREFIX [--execute]" >&2; exit 2; fi
source_dir=$(cd "$1" && pwd)
prefix=$(cd "$2" && pwd)
export PKG_CONFIG_LIBDIR="$prefix/lib/pkgconfig:$prefix/share/pkgconfig"
unset PKG_CONFIG_PATH
args=(setup "$source_dir/_build-minimal" "$source_dir" --default-library=shared --buildtype=release --strip --prefix="$prefix"
 -Dauto_features=disabled -Ddeprecated=false -Dexamples=false -Dcplusplus=true
 -Ddocs=false -Dcpp-docs=false -Dmodules=disabled -Dintrospection=disabled
 -Dexif=enabled -Djpeg=enabled -Duhdr=enabled -Dpng=enabled -Dwebp=enabled
 -Dlcms=enabled -Dhighway=enabled -Dzlib=enabled -Dnsgif=true
 -Dppm=false -Danalyze=false -Dradiance=false)
printf 'EXPERIMENTAL command:'; printf ' %q' meson "${args[@]}"; printf '\n'
echo 'Required staged deps: glib/gio/gobject, libffi, gvdb source, expat, zlib-ng, libexif, lcms2, patched mozjpeg, libpng, libwebp+demux+mux, highway, patched libultrahdr.'
echo 'Reuse exact official compiler optimization/CPU flags, source patches and dependency versions. No substitution is accepted without full differential validation.'
if [ "${3:-}" != --execute ]; then exit 0; fi
for pc in glib-2.0 gio-2.0 gobject-2.0 expat zlib libexif lcms2 libjpeg libpng libwebp libwebpmux libwebpdemux libhwy libuhdr; do pkg-config --modversion "$pc"; done
meson "${args[@]}"
echo 'Configuration only. Build separately with explicitly coordinated -j1; do not change the production hash pin.'
