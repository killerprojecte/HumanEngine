#!/bin/sh
# Build and run the actual provider test binary without Node, TS, ICU files,
# system ICU shared libraries, or source files in its working directory.
set -eu
if [ "$#" -ne 2 ]; then echo "usage: $0 ICU_WORK_DIR EMPTY_OUTPUT_DIR" >&2; exit 2; fi
icu=$(realpath "$1")
mkdir -p "$2"
out=$(realpath "$2")
[ -z "$(ls -A "$out")" ] || { echo 'Output directory must be empty' >&2; exit 2; }
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
. "$icu/env.sh"
cd "$root"
for package in topiccollation jsdate nodeicu; do
  go test -c -tags humanengine_icu -o "$out/$package.test" "./internal/$package"
done
mkdir "$out/testdata" "$out/licenses"
cp internal/topiccollation/testdata/node.json "$out/testdata/node.json"
cp internal/jsdate/testdata/oracle.json "$out/testdata/oracle.json"
cp native/icu/LICENSE "$out/licenses/ICU-LICENSE"
cp "$icu/build.json" "$out/native-build.json"
: > "$out/shared-libraries.txt"
for package in topiccollation jsdate nodeicu; do
  ldd "$out/$package.test" >> "$out/shared-libraries.txt"
done
if grep -E 'libicu|libnode' "$out/shared-libraries.txt"; then echo 'Unexpected ICU/Node shared runtime dependency' >&2; exit 1; fi
cd "$out"
for package in topiccollation jsdate nodeicu; do
  env PATH=/nonexistent ICU_DATA=/nonexistent LD_LIBRARY_PATH=/nonexistent LANG=zz_ZZ.UTF-8 LC_ALL=zz_ZZ.UTF-8 "./$package.test" -test.v
done
