# Genuine sqlite-vec bundle

The pinned genuine binary is included and verified from the locked official npm
package. Native execution checks are required independently of source recovery.

The `bundled_sqlite_vec` tag embeds the official sqlite-vec 0.1.9 binary for
Linux/amd64 (glibc). Other targets/builds return sqlite_vec_bundle_unavailable
before creating an installation. There is no Node fallback, runtime download,
source lookup, scalar-function simulation or vector-table simulation.

`manifest.json` records the upstream npm URL/integrity, exact binary SHA-256,
version, target and license provenance. `LICENSE-MIT` reproduces the upstream
v0.1.9 MIT license from the prior verified fetch. Redistribution selects MIT from
the package's declared `MIT OR Apache` alternatives. Both license and manifest
are embedded and should additionally ship as readable attribution sidecars.

Expected binary: 159,816 bytes, SHA-256
`5923730861b86c707cca5602b5f91092f9e52a46706dbc6e269fd4bb9c4498e8`.
Previous static ELF inspection reported libc.so.6 and GLIBC versions through
2.14. This fact has not been rechecked after workspace recovery. No musl,
static-only, macOS, Windows or ARM support is claimed.

## Reproducible preparation and integration

After lockfile-driven npm installation, from backend:

    python3 scripts/prepare-sqlite-vec.py --repository ..
    python3 scripts/prepare-sqlite-vec.py --repository .. --check

The script performs no network or subprocess action. It checks the exact parent
optional dependency, package identity/version, lockfile resolved URL/integrity,
ELF target and SHA-256 before atomically staging the binary. With no repository
argument, --check verifies already vendored assets. Pins/platform additions
require review; there is no switch to accept unreviewed current bytes. npm ci
checks tarball SRI; this script additionally checks extracted library bytes.

Go-only build integration can use:

    python3 scripts/prepare-sqlite-vec.py --check
    go test -tags sqlite_fts5,bundled_sqlite_vec ./internal/vectorasset ./internal/installation
    sh tests/installation-source-free.sh

No live Makefile or production command is changed. These commands form the
suggested installation-bundle-check target. Add bundled_sqlite_vec separately
to production bundle build tags; bundled_wasm does not imply native vectors.
Call installation.OpenBundled with Database, AuthorityDirectory and explicit
SourceTimezone; leave VectorExtension empty. Explicit path overrides conflict
with bundle selection. installation.Open remains the explicit-library API.
The production runtime/HTTP facade remains unintegrated by this package.

Native ICU remains independent: add humanengine_icu and the existing pinned ICU
headers/libraries/data configuration for named timezone support. The source-free
script accepts an absolute GO executable and HUMANENGINE_EXTRA_BUILD_TAGS, while
compilation inherits CGO build flags. Runtime receives a clean environment.

## Lifecycle

Each Store gets a new 0700 temporary directory and exclusively created 0600
library file. Embedded bytes are checked, written, flushed and closed before
loading. Every physical connection loads under an Asset.Use read lease. Close
uses the exclusive lease so it cannot remove a file during dlopen. Store closes
the authority and main pool, then removes only its own extraction. Startup
failure cleans that extraction without deleting main or durable authority data.
Linux loaded mappings survive unlink. Already checked-out SQL connections still
belong to their caller and must be closed/returned normally.

No shared cache, sibling-directory reaping, runtime download or resource cap is
introduced. TMPDIR must permit native shared-library mapping; loader/noexec
failures return an error without fallback. Crashes can leave a private temp
directory, which ordinary OS temporary cleanup handles.

## Source-free proof scope

The script builds one test executable, then runs its bundled installation test
in a fresh directory without source, node_modules, Go or Node in PATH and with a
scrubbed environment. It exercises genuine vec0 insertion/read/update, replaced
physical connection hooks, automatic baseline, restart and cleanup. A skip does
not count as PASS. System loader/libc remain available. This is a no-source-
lookup runtime proof, not mount-namespace denial of the host filesystem.

## Recovery completion update

The genuine binary has now been restored from the newly installed locked npm
package and verified against the retained SHA-256. It is present in this overlay,
not pending download. Node source oracle fixtures were regenerated separately.
Go compilation, runtime extraction tests and source-free execution remain pending;
no validation result from the lost execution environment is inherited.
