#!/bin/sh
# Functional proof only: this temporary test bundle is not a completed release.
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
: "${HUMANENGINE_VIPS_LIBRARY:?point to the verified libvips artifact}"
: "${HUMANENGINE_IMAGE_PROOF_DIR:?use a new durable proof directory}"
case "$HUMANENGINE_IMAGE_PROOF_DIR" in /*) ;; *) echo 'proof directory must be absolute' >&2; exit 1;; esac
if [ -e "$HUMANENGINE_IMAGE_PROOF_DIR" ]; then echo 'proof directory already exists; do not overwrite evidence' >&2; exit 1; fi
mkdir -p "$HUMANENGINE_IMAGE_PROOF_DIR/runtime" "$HUMANENGINE_IMAGE_PROOF_DIR/home"
cd "$root"
"${GO:-go}" build -buildvcs=false -trimpath -tags sqlite_fts5 -o "$HUMANENGINE_IMAGE_PROOF_DIR/runtime/imagecodec-probe" ./cmd/imagecodec-probe
cp "$HUMANENGINE_VIPS_LIBRARY" "$HUMANENGINE_IMAGE_PROOF_DIR/runtime/libvips-cpp.so.8.18.7"
cp internal/imagecodec/testdata/source.json "$HUMANENGINE_IMAGE_PROOF_DIR/runtime/source-oracle.json"
cp -R internal/imagecodec/distribution/licenses "$HUMANENGINE_IMAGE_PROOF_DIR/runtime/licenses"
cp internal/imagecodec/distribution/manifest.json "$HUMANENGINE_IMAGE_PROOF_DIR/runtime/manifest.json"
cd "$HUMANENGINE_IMAGE_PROOF_DIR/runtime"
if [ "${HUMANENGINE_IMAGE_REQUIRE_TRACE:-0}" = 1 ] && ! command -v strace >/dev/null 2>&1; then
  echo 'required file tracing is unavailable' >&2
  exit 1
fi
env_binary=$(command -v env)
set +e
if command -v strace >/dev/null 2>&1; then
  strace -f -qq -e trace=file -o ../files.trace "$env_binary" -i PATH=/nonexistent HOME="$HUMANENGINE_IMAGE_PROOF_DIR/home" VIPS_CONCURRENCY=1 ./imagecodec-probe -library "$PWD/libvips-cpp.so.8.18.7" -fixtures "$PWD/source-oracle.json" -output "$PWD/native-results.json" > ../runtime.log 2>&1
else
  env -i PATH=/nonexistent HOME="$HUMANENGINE_IMAGE_PROOF_DIR/home" VIPS_CONCURRENCY=1 ./imagecodec-probe -library "$PWD/libvips-cpp.so.8.18.7" -fixtures "$PWD/source-oracle.json" -output "$PWD/native-results.json" > ../runtime.log 2>&1
fi
status=$?
set -e
printf '%s\n' "$status" > ../runtime.exit
cat ../runtime.log
[ "$status" -eq 0 ] || exit "$status"
if [ -f ../files.trace ]; then
  python3 "$root/tests/check-image-trace.py" ../files.trace "$(CDPATH='' cd -- "$root/.." && pwd)" "$env_binary"
fi
readelf -d imagecodec-probe > ../executable-dynamic.txt
readelf -d libvips-cpp.so.8.18.7 > ../library-dynamic.txt
if [ -f ../files.trace ]; then
  printf '%s\n' 'PASS: standalone image artifacts with file trace checked for development source/dependency lookups.'
else
  printf '%s\n' 'PASS: standalone functional run with empty PATH; file-access tracing was unavailable.'
fi
printf '%s\n' 'Complete binary redistribution licensing remains a separate gate.'
