#!/bin/sh
# Run from backend after provisioning native ICU and the embedded SQLite vector.
set -eu

tags=sqlite_fts5,humanengine_icu,bundled_wasm,bundled_sqlite_vec
case "$1" in
    compatibility)
        make plugin-layout-check wasm
        ;;
    without-snowluma)
        tags=$tags,without_snowluma
        make wasm WASM_PLUGINS='typing-delay web-read web-search'
        make unbundled-plugin-graph-check SNOW_FREE_TAGS="$tags"
        ;;
    external-only)
        tags=sqlite_fts5,humanengine_icu,bundled_sqlite_vec
        make external-only-plugin-graph-check
        ;;
    *)
        printf 'Unknown preview plugin profile: %s\n' "$1" >&2
        exit 2
        ;;
esac
go build -trimpath -buildvcs=false -tags "$tags" -o "$2" ./cmd/humanengine
