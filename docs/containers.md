# Docker Compose and GHCR

The official image is `ghcr.io/killerprojecte/humanengine`. The container workflow builds Linux **amd64** with Node **24.22.1 LTS** on Debian bookworm, checks the application, starts the exact candidate with Compose, and only then publishes it. ARM64 is not yet a verified image target. Build and runtime use the same Node/libc ABI; native SQLite, sqlite-vec and sharp are exercised inside the final image, not just on the build host.

`latest` follows successful master builds. For reproducible updates, select the full `sha-<commit>` tag or the immutable digest printed in the workflow summary. Do not infer successful publication from a commit alone: check the Container workflow outcome.

## Start

Install Docker Engine with Docker Compose **2.24 or later**. Obtain `compose.yaml` and `.env.example` from the same tested repository revision, or clone the repository:

```sh
cp .env.example .env
# Edit .env locally: set a long random HUMANENGINE_ADMIN_TOKEN and provider secrets.
# Optional: set HUMANENGINE_IMAGE=ghcr.io/killerprojecte/humanengine:sha-<full-commit>
docker compose pull
docker compose up -d --no-build
docker compose ps
```

Open `http://127.0.0.1:3000` and enter the administration token. The published port is bound to host loopback; the process listens on `0.0.0.0` only **inside** its container. `HUMANENGINE_PORT` changes the host port. No remote access, public hosting, TLS termination, or deployment is provisioned by the workflow.

A new GHCR package is private by default; this project does not change its visibility. If pulling requires authentication, an authorized user can run `docker login ghcr.io` using their own existing credential with `read:packages` and repository/package access, according to their account policy. Do not paste credentials into chat, shell history, image build arguments, or this repository. No persistent token is created by the workflow; publishing uses its short-lived `GITHUB_TOKEN`. A permission error requires the owner to confirm package/repository access, not to make the package public.

## Configuration and storage

- `.env` stays on the host, excluded from build context and Git. Compose injects it at runtime, including custom provider credential-variable names. Set `HUMANENGINE_ENV_FILE` for a different runtime env-file path; Compose interpolation still uses the shell or its `--env-file` option. Protect env files with owner-only permissions. Docker administrators can inspect container environments; use a trusted host and never share `docker compose config` output containing secrets.
- The named `humanengine-data` volume holds `/app/data`: SQLite configuration/memory/provenance, original media, and bounded technical logs. Environment secrets are not stored in SQLite or baked into images. The application does not restore raw conversation caches, outgoing queues, or temporary simulations after restart.
- The image runs as the unprivileged `node` user, with a read-only root filesystem, writable data volume and temporary directory, dropped capabilities and no privilege escalation. Named volumes receive the image directory's ownership. Existing bind mounts must be writable by UID/GID 1000; do not solve a permission error by running the application as root.
- The authenticated health check queries `/api/status` without printing its token. Unauthenticated API access remains rejected; no separate unauthenticated management endpoint is added. Compose sends SIGTERM and gives the engine 30 seconds to drain shutdown.
- Builtin SnowLuma configuration stays in SQLite and secrets remain environment references. A WebSocket URL using `127.0.0.1` refers to the **container**, not the Docker host: configure a reachable private endpoint appropriate to your platform. No host network mode or automatic platform connection is enabled.
- Trusted local `.mjs` plugins are intentionally excluded from images. To use them, create a host plugin directory, enable the documented read-only `/app/plugins` mount in Compose, and list filenames in `HUMANENGINE_PLUGINS`. Untrusted plugins remain unsupported. Static custom-page assets must be included in a reviewed custom build's `public` directory.

## Update, stop, and local build

Back up configuration and media before upgrading. See [operations](operations.md) for consistent SQLite backup/fresh restore; do not copy a live SQLite main file alone and omit its WAL. `docker compose stop` stops without removing data. `docker compose down` retains the named volume; **`docker compose down --volumes` destroys it** and is not an update step.

```sh
docker compose pull
docker compose up -d --no-build
# Inspect health without dumping environment variables:
docker compose ps
```

To build reviewed source locally instead of pulling, set `HUMANENGINE_IMAGE=humanengine:local` in `.env`, then run `docker compose build` and `docker compose up -d --pull never`. Build context excludes local data, secrets and plugins. The build stage has compiler tools for native fallback; the runtime contains only the built server and its required native packages, not the Nuxt build toolchain.

## Verification and limits

`node scripts/container-smoke.mjs <locally-loaded-image>` creates a uniquely named disposable Compose project. It verifies authenticated health, unauthenticated rejection, nonroot native SQLite/vector/image processing, settings and media persistence through restart **and recreation**, and graceful shutdown. Cleanup removes only that test project's volume. The workflow publishes the exact tested image, pulls its registry digest back, and checks its image identity before advancing `latest`.

Local authoring environments without Docker cannot claim local container execution; the GitHub-hosted workflow provides the actual image/Compose run. Live provider and QQ compatibility remain separate checks. Upstream dependency risks and observed production exposure are tracked in [security notes](../SECURITY.md), not represented as a clean audit.

References: [GitHub image publishing](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images), [GHCR access](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry), [Compose environment configuration](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/), [official Node images](https://hub.docker.com/_/node).
