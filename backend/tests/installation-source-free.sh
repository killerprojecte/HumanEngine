#!/bin/sh
# Build only at explicit invocation. Runtime proof receives the test executable
# alone in a clean directory with no Node/Go/source/dependency lookup environment.
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/humanengine-installation-free.XXXXXXXX")
trap 'rm -rf "$out"' EXIT HUP INT TERM
cd "$root"
python3 scripts/prepare-sqlite-vec.py --check
mkdir "$out/runtime" "$out/tmp" "$out/home" "$out/empty-path"
tags="sqlite_fts5,bundled_sqlite_vec"
if [ -n "${HUMANENGINE_EXTRA_BUILD_TAGS:-}" ]; then tags="$tags,$HUMANENGINE_EXTRA_BUILD_TAGS"; fi
"${GO:-go}" test -c -tags "$tags" -o "$out/runtime/installation.test" ./internal/installation
# Ship the attribution as a user-readable sidecar too; it is also embedded.
cp internal/vectorasset/LICENSE-MIT "$out/runtime/sqlite-vec-LICENSE-MIT"
cp internal/vectorasset/manifest.json "$out/runtime/sqlite-vec-manifest.json"
cd "$out/runtime"
set +e
env -i PATH="$out/empty-path" HOME="$out/home" TMPDIR="$out/tmp" HUMANENGINE_SOURCE_FREE_STAGE=1 ./installation.test -test.v -test.run '^TestBundledInstallationSourceFree$' > "$out/result.log" 2>&1
status=$?
set -e
cat "$out/result.log"
[ "$status" -eq 0 ] || exit "$status"
grep -F -- '--- PASS: TestBundledInstallationSourceFree ' "$out/result.log" >/dev/null
printf '%s\n' 'PASS: genuine embedded sqlite-vec installation, physical reconnect and restart with no Node/Go/source dependency lookup'
