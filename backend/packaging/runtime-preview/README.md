# Opt-in native runtime-preview package

This is a **local/CI candidate**, not the default production image, a full API
replacement or a distribution release. The root Dockerfile and Compose file are
unchanged. Only Linux amd64 has verified image/vector assets. This recipe uses
Go 1.27.3, Node 24.22.1, the committed npm lockfile, the existing hash-verified ICU
builder and pinned libvips/sqlite-vec artifacts. All stages use Debian Trixie:
its [Python 3.13 package](https://packages.debian.org/trixie/python3) supports the
ICU builder's unchanged `TarFile.extractall(filter='data')`; keeping the native
build and runtime on the same distribution avoids newer CGo/ICU libc or libstdc++
requirements being copied into an older runtime. The exact
[Node tag](https://hub.docker.com/v2/repositories/library/node/tags/24.22.1-trixie-slim)
and [Go tag](https://hub.docker.com/v2/repositories/library/golang/tags/1.27.3-trixie)
were verified to include linux/amd64. This source verification is not a Docker
build or container acceptance pass. Base-image tags and Debian
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

Open **http://127.0.0.1:3001** (override `HUMANENGINE_PREVIEW_PORT`) and enter
the preview administration token. This is the existing Nuxt UI built as static
client files. The Go process serves those files and owns all same-origin
`/api` requests, including browser sessions; no Nitro server or TypeScript
database worker runs. An unsupported API stays a Go error, never the UI shell
or a TypeScript fallback. Preview data, authority and plugin volumes are separate. Do not point this at the
production installation. The existing preview basename remains `/app/data/main.sqlite` so existing
preview volumes keep their data. For a disposable TS installation copy, use an
explicit command override with `--database /app/data/humanengine.sqlite`
instead; pointing that copy at the preview default would create a second
database. Both matching database and authority are required for an existing
test copy; never replace its authority with an old backup. The token
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
- static Nuxt document, hashed client asset and direct page navigation through
  the same origin as Go; real cookie login, protected dashboard API reads,
  cookie-authenticated settings write and logout/revocation, unchanged
  unsupported API errors and no static fallback for API paths. The package
  driver checks HTTP responses; it does not run a browser or certify all UI pages;
- zero enabled V1 plugins with no retained Node process (no Node healthcheck);
  the observer runs as the normal image user inside its PID namespace and matches
  `/proc/<pid>/exe` against the canonical Node executable, emitting PIDs only.
  It never reads command lines or environments: Node 24 can report `MainThread`
  in `comm`, so process names are not executable identity;
- second writer rejection using the same data/authority on a different published
  port, while the first instance remains usable;
- real HTTP `100 Continue` before SIGTERM, waiting on the held body, accepted write
  completion before owner drain, and persistent settings after restart;
- the same standalone synthetic V1 fixture mounted under two distinct plugin IDs:
  zero Node after disabled startup settles; exactly one PID after first enable
  and echo; that same sole PID after second enable; first disabled action returns
  unavailable while the second still echoes on that PID; zero after last disable.
  This proves shared-host lifecycle only when the actual Docker gate passes.

The resulting JSON records the exact image ID and completed checks; synthetic
container logs are retained without token values. The parameterized fixtures have no model,
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

## Opt-in hosted Chromium smoke

The browser smoke activates only on an existing `feat/refactor-golang` push
whose before/after diff changes `browser_smoke.py` or `browser-requirements.txt`, or
whose exact head commit opts into both proofs below. Pull requests and manual runs
do not install or start Chromium. This branch-local push
path does not require the workflow to exist on the default branch. The existing job builds the real preview image and
launches it against only disposable synthetic data. In addition to the normal
HTTP and lifecycle checks, a sandboxed, host-side Chromium page uses the real
Nuxt login form, reads dashboard data, edits and saves the name through the
Settings page, reloads to verify the write, and logs out. No API is stubbed;
the runner blocks browser traffic away from the preview origin. No browser or
driver is added to either runtime image or to ordinary push/PR package runs.

The host driver wheels are hash-locked in `browser-requirements.txt`. The
[Playwright Chrome channel](https://playwright.dev/python/docs/browsers#google-chrome--microsoft-edge)
selects the runner's preinstalled Chrome Stable, with its sandbox enabled and
no browser download or fallback. Chrome's observed version belongs to the
evidence, not the lockfile; the exact driver/runner-browser pairing is not
qualified until the hosted test passes. Launch failures record only a fixed
category and never the exception text. The existing
`go-package-preview-evidence` artifact contains the OCI image ID, revision,
browser versions and sanitized action/status receipts in `result.json`. Opted-in
runs do not retain container logs, screenshots, traces, HTML or request bodies.

## Run both proofs on one exact candidate

For a push to the existing `feat/refactor-golang` branch that already matches the
workflow paths, add this Git trailer to the final candidate commit, separated
from the commit body by a blank line:

```text
HumanEngine-Package-Proofs: all
```

The workflow reads only the pushed head commit's trailers, not earlier commits
or commit-body prose. It runs Chromium and the migration/restore drill against
the same built image, and saves both sanitized receipts. This does not publish
the image or change startup defaults. Pushes without the trailer retain the
separate diff-based triggers.

## Rehearse engine rollback only on a disposable clone

1. Record the exact TS and Go image IDs and existing mounts. Stop and drain the
   TS writer first. Cold-copy the complete synthetic data mount and its
   matching independent current authority, preserving keys and media. Keep
   `/app/data/humanengine.sqlite` and `/app/authority` at the same in-container
   paths. An older authority must not be put back over the current one.
2. Open the copy with Go only. Read a distinctive TS-created record, make a
   harmless durable change and confirm the same basename and authority. Stop
   Go with SIGTERM, require exit 0 and `app.drained`, then start TS alone on
   the latest matching pair; it must recognize the change and continue
   authenticating after a fresh login.
3. If TS cannot open the latest pair with a verified reader, stop there. Use
   supported offline authority-aware restore/reconciliation with the latest
   authority; do not swap just the old database back, open both writers, or
   silently replay uncertain external effects.

A source/binary-level cold-copy rehearsal is not a Docker migration guarantee.
Neither this package driver nor the public UI proves production rollback,
backup/recovery, platform adapters, or the remaining API contract.

## Validation and release limitations

The identity-probe correction includes Python driver tests, a real Node 24.22.1
subprocess with `comm=MainThread` inspected as an ordinary non-root user, and both
fixture IDs imported/executed by real Node. These are local probe/fixture checks,
not a shared-host or container acceptance result. Docker is unavailable in the
implementation workspace, so **image build and end-to-end container acceptance
have not run locally**. The final commit's Docker gate remains required; no skip
or mock fallback turns a missing Docker runtime into success.

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
