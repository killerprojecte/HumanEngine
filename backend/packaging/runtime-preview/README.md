# Opt-in native runtime-preview package

This is a **local/CI candidate**, not the default production image, a full API
replacement or a distribution release. The root Dockerfile and Compose file are
unchanged. Only Linux amd64 has verified image/vector assets. This recipe uses
Go 1.27.3, Node 24.22.1, the committed npm lockfile, the existing hash-verified ICU
builder and pinned libvips/sqlite-vec artifacts. Base-image tags and Debian
packages are not immutable byte-for-byte image inputs; retain the resulting image
ID and artifact hashes with each acceptance result.

## Build and run on disposable data

From repository root, with Docker and Compose available:

```sh
docker build --platform linux/amd64 \
  -f backend/packaging/runtime-preview/Dockerfile \
  -t humanengine-go-preview:local .
python3 backend/packaging/runtime-preview/acceptance.py \
  --evidence /tmp/humanengine-package-evidence
```

For an interactive preview, supply `HUMANENGINE_ADMIN_TOKEN` in your environment
and run the independent Compose project:

```sh
docker compose -f backend/packaging/runtime-preview/compose.yaml up -d
```

It listens on loopback port 3001 (override `HUMANENGINE_PREVIEW_PORT`) and creates
separate preview data, authority and plugin volumes. Do not point this at the
production installation. Both matching database and authority are required for
an existing test copy; never replace its authority with an old backup. The token
is not embedded in the image. Set installation-specific locale/timezone via an
explicit command override using the launcher flags in
[the runtime-preview guide](../../docs/runtime-preview.md).

The Go executable is PID 1 and owns shutdown. Do not infer successful draining
from a container being killed. For a deliberate graceful stop without a forced
Compose stop deadline:

```sh
id=$(docker compose -f backend/packaging/runtime-preview/compose.yaml ps -q runtime-preview)
docker kill --signal=TERM "$id"
docker wait "$id"
docker logs "$id"
```

Check exit 0 and the real `app.drained` receipt before considering the installation
closed. On an unfinished drain, retain state and investigate; this package does
not add a production deadline or forcibly close owners. The acceptance harness
has test-only deadlines and force-removes **its own disposable fixtures** on failure.

## What the acceptance harness proves when it passes

It drives the installed image, never a checkout's Go test server:

- unauthenticated refusal and authenticated settings write/read;
- zero enabled V1 plugins with no retained Node process (no Node healthcheck);
- second writer rejection using the same data/authority on a different published
  port, while the first instance remains usable;
- real HTTP `100 Continue` before SIGTERM, waiting on the held body, accepted write
  completion before owner drain, and persistent settings after restart;
- standalone synthetic V1 module mounted without the source tree, enabled through
  the real API, executing an admin action, then physically unloading the last host.

The resulting JSON records the exact image ID and completed checks; synthetic
container logs are retained without token values. The fixture has no model,
platform, or paid-provider effects. No volume or container outside its unique
per-run prefix is removed. The image embeds V1 workers, ICU and builtin WASM;
Node is available for V1 but is not the server entrypoint. Local V1 plugins with
external npm dependencies must provide their own dependency tree; this fixture
does not establish arbitrary SDK/module compatibility.

The scoped `Go packaged runtime preview` workflow builds and tests on packaging
changes or explicit dispatch, with read-only repository permissions. It does not
publish an image or deploy anything. Unrelated backend changes still require
rerunning this gate before release; a previous image's result is not current-head
acceptance. Default Go migration CI remains a separate gate.

## Validation and release limitations

The initial implementation was checked with Python driver tests, syntax/format
checks and source review. Docker is unavailable in the implementation workspace,
so **image build and end-to-end container acceptance have not run locally**. The
workflow must pass on the final commit before this slice is accepted; no skip or
mock fallback turns a missing Docker runtime into success.

The image carries the repository's available imagecodec source/notices/provenance,
ICU license/build metadata and sqlite-vec license/manifest. This does **not** close
[the native image redistribution gate](../../internal/imagecodec/distribution/RELEASE-GATES.md):
its manifest still records incomplete corresponding sources/notices. Do not
publish the image as a distributable release until that independent gate and the
native dependency/relinking instructions are verified. The build does not claim
all bundled dependencies' legal materials are complete.

Full WebSocket/HTTP 101, platform/media coverage, the three changed main-branch
runtime contracts, complete migration/restore and whole-system load acceptance
remain separate work in [the handoff](../../../docs/golang/remaining-work.md).
