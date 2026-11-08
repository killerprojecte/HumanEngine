# Executable source/notices acquisition

All commands below are prepared, not blanket permission to download/build now.
They run from this source directory. Tools never invoke native compilers unless
explicitly running the experimental configure script. Fetch defaults to dry-run.

## 1. Inspect and budget

python3 tools/fetch_sources.py --manifest acquisition-complete-plan.json --dest downloads/native
python3 tools/fetch_sources.py --manifest rust-acquisition-manifest.json --dest downloads/rust

The active glibc plan has 33 source/patch inputs, including GLib's pinned GVDB
fallback. They were retrieved (163,273,618 bytes) and their observed hashes are
in native-source-lock.json. The archives and transient availability reports are
not shipped in this source-only checkpoint. Retrieval records do not establish
historical binary-build identity or complete license delivery.

The original upstream librsvg lock includes 357 registry crates with cryptographic
archive checksums. All 357 HEAD requests returned 200 with a combined reported
size of 38,675,419 bytes. rust-trusted-lock.json pins those archive hashes; the actual
post-patch build may use a subset. Do not equate this conservative vendor source
set with every linked crate or the final post-patch lock.

## 2. Fetch when the byte budget is explicitly approved

The included native lock pins observed archive hashes. Bootstrap mode remains
available for explicit future review; it does NOT prove historical build
identity or authenticity of mutable tags/PR patches. Use a chosen budget rather
than copying a guessed unlimited value:

python3 tools/fetch_sources.py --manifest acquisition-complete-plan.json --dest downloads/native --download --bootstrap-lock native-source-lock.json --max-total-bytes APPROVED_TOTAL --max-file-bytes APPROVED_SINGLE_FILE

Reconcile the bootstrap lock against upstream release checksums/immutable commits,
including every patch, then repeat with --lock native-source-lock.json for strict
hash verification. The tool will not retry denied or partial downloads silently.
Inspect partial files before explicitly resuming an authorized operation.

Rust archives already have Cargo.lock checksums:

python3 tools/fetch_sources.py --manifest rust-acquisition-manifest.json --dest downloads/rust --download --lock rust-trusted-lock.json --max-total-bytes APPROVED_TOTAL --max-file-bytes APPROVED_SINGLE_FILE

The downloads/rust/verified-downloads.json output records filenames and hashes for
notice collection. The actual post-patch librsvg tree can alternatively be passed
to tools/vendor_rust_sources.sh with an already available pinned Cargo toolchain;
that performs dependency retrieval, not compilation, and must be separately budgeted.

## 3. Extract full copyright/license material safely

python3 tools/collect_notices.py --lock native-source-lock.json --archives downloads/native --output collected-notices/native
python3 tools/collect_notices.py --lock downloads/rust/verified-downloads.json --archives downloads/rust --output collected-notices/rust

This copies legal files from verified archives without extracting archive paths or
executing code. It preserves copyright text unchanged and records original paths,
archive hashes and notice hashes. It fails on components with no discovered legal
file. Follow with explicit review of SPDX declarations, embedded libnsgif/GVDB,
Rust crates, profile data, patched code and notices required by dual-license choices.
Filename matching is useful collection, not a compliance decision.

Retain all complete source archives, exact patches, upstream build recipe/toolchain
files, post-patch Cargo.lock/vendor sources, resulting notice manifest, LGPL/GPL
texts and the bridge's Apache adaptation notice. Deliver these alongside the native
binary at a real stable location or inside the same source-material archive. Do
not substitute a list of URLs, an invented source offer, or the upstream short
license inventory for this material.

## 4. Source-only checkpoint now

python3 tools/prepare_source_checkpoint.py OVERLAY_ROOT OUTPUT.tar.gz

This explicitly omits .so/ELF runtime files and cannot be mistaken for the full
native distribution. It includes a hash manifest and the current incomplete-status
reports. It never overwrites the parent's source-free script or earlier native logs.

## 5. Verify complete material coverage before packaging

python3 tools/verify_source_materials.py --manifest acquisition-complete-plan.json --lock native-source-lock.json --archives downloads/native --notices collected-notices/native
python3 tools/verify_source_materials.py --manifest rust-acquisition-manifest.json --lock downloads/rust/verified-downloads.json --archives downloads/rust --notices collected-notices/rust

These verify requested-input coverage and content hashes. They do not replace
review of LGPL source correspondence, complete copyright notices and the actual
distribution delivery method. The checked scripts passed Python/shell syntax
checks plus a no-network smoke test over the already retrieved exact packaging
source archive; no large dependency archive was fetched for the smoke test.
