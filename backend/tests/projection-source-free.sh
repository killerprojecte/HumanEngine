#!/bin/sh
# Exercise real guarded FTS5 in a compiled test binary from an empty directory.
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/humanengine-projection-free.XXXXXXXX")
trap 'rm -rf "$out"' EXIT HUP INT TERM
cd "$root"
go test -c -tags sqlite_fts5 -o "$out/projections.test" ./internal/projections
cd "$out"
set +e
env PATH=/nonexistent ./projections.test -test.v -test.run '^TestRealFTSGuardedRootChildReopen$' > "$out/result.log" 2>&1
status=$?
set -e
cat "$out/result.log"
[ "$status" -eq 0 ] || exit "$status"
grep -F -- '--- PASS: TestRealFTSGuardedRootChildReopen ' "$out/result.log" >/dev/null
echo 'PASS: real guarded FTS5 operates without Node, Go compiler or source tree'
