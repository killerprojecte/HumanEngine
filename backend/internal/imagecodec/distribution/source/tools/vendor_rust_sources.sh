#!/usr/bin/env bash
# Run only after source downloads/toolchain work are explicitly scheduled.
# No tool installation, compiler invocation, or native build is performed here.
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "Usage: $0 EXACT_POST_PATCH_LIBRSVG_TREE OUTPUT_DIRECTORY" >&2; exit 2; fi
source_dir=$(cd "$1" && pwd)
mkdir -p "$2"
output_dir=$(cd "$2" && pwd)
test -f "$source_dir/Cargo.lock" || { echo "Missing exact post-patch Cargo.lock" >&2; exit 1; }
command -v cargo >/dev/null || { echo "Pinned Cargo toolchain must already be available" >&2; exit 1; }
if [ -e "$output_dir/vendor" ]; then echo "Refusing to overwrite vendor output" >&2; exit 1; fi
cp "$source_dir/Cargo.lock" "$output_dir/Cargo.lock.before"
(cd "$source_dir" && cargo vendor --locked "$output_dir/vendor") > "$output_dir/cargo-vendor-config.toml"
cmp "$source_dir/Cargo.lock" "$output_dir/Cargo.lock.before"
cp "$source_dir/Cargo.lock" "$output_dir/Cargo.lock"
cargo --version > "$output_dir/cargo-version.txt"
find "$output_dir/vendor" -type f -print0 | sort -z | xargs -0 sha256sum > "$output_dir/vendor-files.sha256"
echo "Vendor source fetched without building; reconcile every crate license/SPDX notice before release."
