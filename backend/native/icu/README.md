# Exact native Node collation dependency

This package pins **Node v24.22.1's vendored ICU source and full data**, not merely
an ICU version string. Upstream ICU 78.3 advertises CLDR 48.2 changes; the source
Node oracle has ICU 78.3 / CLDR 48.0 / Unicode 17.0. Substituting the upstream
release or host ICU would not establish parity.

## Provenance and licensing

`provenance.json` records official source URLs and SHA-256 fingerprints. The
55 MiB Node archive was checked against the official HTTPS `SHASUMS256.txt` before
extraction. No GPG signature verification is claimed. `sources.sha256` covers all
1,065 files in Node's `deps/icu-small` subset. Despite that directory's name, its
README identifies it as the source used by `--with-intl=full-icu`; its compressed
`source/data/in/icudt78l.dat.bz2` contains the full locale data package.

The uncompressed package is exactly 33,107,952 bytes with SHA-256
`f294f56707f4bbbc669d291ff537f632eb8d363a8ea60b70b091cdf273ea0499`.
Independent read-only verification of the observed Linux/amd64 Node oracle
executable found this entire exact data package once, at byte offset 70,289,072.
That executable's SHA-256 is
`61ab14cd750fc24d5db53490ec5fc40f756e4b6e136b78cf7004265471d9bd1d`.
This observation is recorded in `provenance.json`; it is not a claim that other
platforms or Node builds share that binary digest or data offset.

The complete vendored ICU/Unicode third-party license is in `LICENSE`. Include
that file in future Go runtime distributions. Do not rely on the TypeScript
image or Node installation to supply the dependency or its license.

## Reproducible build recipe

Current verified target: **Linux little-endian ELF, linux/amd64**. No system
installation, package-manager ICU, Node runtime, network access during the build,
or vendored ICU binary is required. The build compiles only the vendored common
and i18n source files and embeds the exact data as aligned read-only ELF bytes.
It follows the relevant `tools/icu/icu-generic.gyp` flags (static implementation,
no ICU service registry/dynamic loading, C++ standard string support, break
iteration enabled) and uses deterministic archive order/timestamps and mapped
source paths. Compiler/tool versions and exact commands are recorded in
`WORK_DIR/build.json`. Reproducibility is scoped to the same compiler, target and
flags; cross-compiler byte identity is not claimed.

Download the archive from the `sourceArchive` URL in `provenance.json` and verify
its digest against the `officialChecksums` URL. Then, from the repository root:

```sh
python3 backend/native/icu/build.py \
  --archive /absolute/path/node-v24.22.1.tar.xz \
  --work-dir /absolute/path/icu-build
. /absolute/path/icu-build/env.sh
cd backend
go test -race -tags humanengine_icu ./internal/topiccollation ./internal/topicroute
go vet -tags humanengine_icu ./internal/topiccollation ./internal/topicroute
```

`--prepare-only` verifies and safely extracts without compiling. The builder
rejects archive checksum mismatches, traversal and archive links/devices, then
checks every source and the decompressed data. Compilation is serial. On the
verified Debian GCC 14.2.0 environment it took 184 seconds; the archive, selected
sources, objects and static library occupied about 216 MiB together. The static
library is about 46 MiB. These are observations, not execution/resource caps.
Use workspace-owned output directories, not the system or repository sources.

Object cache identities include the complete verified source/header manifest,
translation-unit checksum, compiler version and command. Completed objects are
published atomically, followed by checksum-bound cache records. Interrupted
compiles cannot replace an existing object with partial output; interruption
between object and record publication cannot validate mismatched bytes. Archives
are built at a fresh temporary path before atomic replacement, so deleted source
members cannot linger. Legacy cache records cause a one-time rebuild.

Run the small cache/archive regression tests without compiling ICU:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B backend/native/icu/test_build.py -v
```

The `humanengine_icu` build tag plus `env.sh` are required. Untagged builds expose
an explicit `ErrUnavailable`, never a Node or approximate Go fallback. All ICU
objects/data are linked into the resulting executable; neither ICU `.so` files
nor external `.dat` files are needed. The C/C++ runtime remains dynamically
linked (`libc`, `libstdc++`, `libm`, `libgcc_s`), so a future runtime image must
provide compatible versions. The existing TypeScript Dockerfile is unchanged.

## Make targets and feature-branch CI

From the repository root:

```sh
make -C backend native-icu-builder-test
make -C backend native-icu-test native-icu-source-free
```

The second command retrieves the official pinned archive only if absent, verifies
both the checked-in pin and official checksum on retrieval, builds ICU serially,
then runs native-tagged race tests/vet for the entire backend and the isolated
source-free proof. `native-icu-build` performs just the verified native build;
`native-icu-fetch` explicitly verifies/retrieves the archive without compiling.

Override `ICU_WORK_DIR` and optionally `ICU_ARCHIVE` to reuse workspace-owned
inputs. The default `backend/.native-build/icu` is ignored by Git. Proof runs get
fresh subdirectories under `ICU_PROOF_ROOT` (default `ICU_WORK_DIR/source-free`),
so repeated checks do not overwrite prior evidence. No binary or source archive
is checked in. For example, with an already downloaded archive:

```sh
make -C backend native-icu-test native-icu-source-free \
  ICU_WORK_DIR=/absolute/path/icu-build \
  ICU_ARCHIVE=/absolute/path/node-v24.22.1.tar.xz
```

`.github/workflows/golang.yml` runs the native targets in its feature-branch
compatibility job using runner-owned temporary storage. `make differential`
checks the native-collation oracle and remaining-authority-write oracle as well
as the existing fixtures. Default server builds/startup and the production
TypeScript Dockerfile remain unchanged.

## Source-free distribution proof

From the repository root, with Go on PATH:

```sh
backend/native/icu/test-source-free.sh /absolute/path/icu-build /empty/output-dir
```

This creates isolated collation, date-validity and shared-initialization test executables, synthetic oracle fixtures,
build provenance and license. It verifies no `libicu` or `libnode` dependency via
`ldd`, then runs with `PATH`, `ICU_DATA` and `LD_LIBRARY_PATH` set to nonexistent
locations and an unrelated invalid `LANG`/`LC_ALL`. All 217,980 comparisons still
pass without Node/TypeScript sources, native headers, or ICU data files in the
working directory. The prior collation-only test executable was about 39 MiB;
the expanded proof also exercises all date oracle zones and shared first use. This is a
**provider distribution proof**, not a completed source-free bot image: runtime
admission/scheduler composition and final production packaging remain separate.

## Provider contract

`topiccollation.New(sourceResolvedLocale)` requires the source deployment's
resolved `Intl.Collator().resolvedOptions().locale`. Never assume the observed
`en-US` is universal, and never silently use the new process's environment.
Capture that deployment setting during migration/configuration. The native
provider validates the explicit BCP47 locale, rejects unknown languages instead
of falling back to the host default, and exposes effective locale/version/
attribute metadata with `Info()`. Locale conversion preflights its ICU output
length and allocates room for the terminating NUL, rather than treating ICU's
suggested fixed locale buffer size as a cap. Regression tests cover the former
157-byte boundary and longer valid private-use tags.

After one process-global SHA-256 verification of the immutable embedded data,
ICU is initialized with filesystem data access disabled. Each opened collator
checks ICU 78.3 / Unicode 17.0 / CLDR 48.0 and V8's forced normalization ON and
tertiary strength. Locale-specific numeric/case-first/punctuation defaults are
left intact, as in pinned `js-collator.cc`. For example Thai ignores punctuation
by default and Danish has a different case-first setting.

`Compare(a,b)` fits `topicroute.LocaleCompare` directly. It uses lossless
`jsjson.UTF16` and `ucol_strcoll` with explicit signed-32-bit lengths, preserving
embedded NUL and lone surrogates. Invalid canonical WTF-8 and lengths exceeding
the native ABI are errors, not truncation. `Close` is idempotent, synchronizes
with comparisons, and rejects future comparisons. No Node process is involved;
the lazy V1 plugin runtime remains independent.

## Differential coverage

`backend/tests/generate-topic-collation-fixtures.mjs` is a test-only pinned Node
oracle. Its 14,532 string pairs cover every lone surrogate, supplementary/new
Unicode characters, contractions, ignorables, canonical equivalents, embedded
NUL, long combining sequences, and seeded arbitrary UTF-16. The 15 locale cases
include en-US, German, Swedish, Danish, Thai, Turkish, both Chinese scripts,
Japanese, Korean, Arabic, French, Spanish, traditional Spanish, and German
phonebook collation. Numeric/case-first/punctuation attributes are checked
against Node's resolved options as well as comparison results.

`topicroute/admission_icu_test.go` runs all 2,097 pinned actual-source admission
decisions with this real comparator instead of the finite lookup fixture.
Focused native tests, race tests, vet, no-native failure behavior, fixture
regeneration and the source-free proof passed. This extensive differential suite
supports the exact source/data construction; a finite corpus alone is not a
mathematical proof over every Unicode string or every locale.
