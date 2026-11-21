# Opt-in packaged migration and restore drill

This is a disposable Linux/amd64 operator/CI rehearsal driver, not a shipping
or upgrade command. It does not build, install, publish or deploy by itself.
Use a Docker Engine supporting `volume-subpath`. No browser is needed.

1. From a clean checkout at TypeScript commit
   [39d8deb8922684298bab8ecb139be04555e8474a](https://github.com/killerprojecte/HumanEngine/commit/39d8deb8922684298bab8ecb139be04555e8474a),
   build its unchanged root Dockerfile twice:
   ```sh
   revision=$(git rev-parse HEAD)
   test "$revision" = 39d8deb8922684298bab8ecb139be04555e8474a
   docker build --platform linux/amd64 --label org.opencontainers.image.revision="$revision" \
     -t he-drill-ts .
   docker build --platform linux/amd64 --target build \
     --label org.opencontainers.image.revision="$revision" -t he-drill-ts-tools .
   ```
   The runtime runs actual Nitro/worker bundles. The tools image is only for the
   pinned repo's actual `scripts/backup.ts` and `scripts/restore.ts`: the
   production runtime image does not ship their source/tsx and is not the CLI
   environment. The build stage also provisions the expected native modules.
2. Use the current preview image built from a clean exact Go revision via its
   existing packaging workflow, or the same local build command. If building
   locally, pass `--build-arg SOURCE_REVISION="$go_revision"`. Use an absent
   evidence file in an existing caller-owned folder:
   ```sh
   python3 backend/packaging/runtime-preview/migration_drill.py \
     --ts-image he-drill-ts --ts-tools-image he-drill-ts-tools \
     --go-image humanengine-go-preview:local --go-revision "$go_revision" \
     --evidence ./migration-drill-result.json
   ```
   Image labels identify the provided builds, not signatures or provenance:
   build them from the exact reviewed clean checkouts. Runtime invocations
   resolve the tags once to immutable local image IDs.

The driver owns a unique disposable Docker volume. Both servers and the offline
tools run without container networking; requests are real authenticated TCP over
in-container loopback. Default non-root image users run both servers; the tool
volume initializer only creates owned empty folders. No ports, production
mounts, local plugin directory, credentials, or model settings are imported.

The TS runtime synthesizes one persisted assertion over HTTP. The driver waits
for each server's SIGTERM exit (and Go’s real drain receipt) before another
writer or maintenance process. The TS backup script snapshots the stopped
installation before Go changes a setting and deletes the assertion via its
terminal admin API. TS then reopens the latest pair and must see both changes.
That is the rollback check, not a restore. Separate cold restore clones the
**current** authority at its original absolute in-container pathname, inspects
the known prior/current states read-only, and runs the original restore script
into a new destination. Opening the restored data with Go must not resurrect the
deleted assertion, must replay the updated `debugModelIO` flag, and must keep
managed model work held. This flag is recorded by a typed
`configuration_flags` authority event. Its replay does not imply that ordinary
post-backup writes are recovered: the authority is not a full replication log.
No recovery acknowledgement or model call is issued.

Only exact source/image IDs and completed outcome names are emitted. Container
logs, data, tokens and request/response bodies are not saved. The hosted package job runs this drill only when a push to the existing
`feat/refactor-golang` branch changes `migration_drill.py` or its test. It reuses
the Go image after ordinary HTTP/package acceptance; it does not activate Chrome
unless that separate proof's script or driver lock is changed too. PRs, ordinary
pushes and manual runs do not build the TypeScript image for this drill. Passing proves only this Linux/amd64
synthetic path, not arbitrary historical schemas, media/secret restoration,
post-backup ledger replay for all event kinds, production rollout, or all APIs.
