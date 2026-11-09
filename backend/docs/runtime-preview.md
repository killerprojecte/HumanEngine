# Explicit writable runtime preview

The normal command still refuses to replace production. `--read-only-preview`
and `--inspect-builtins` retain their previous behavior. `--runtime-preview` is
an explicit, mutually exclusive migration mode, not a declaration of parity.
Docker, package startup scripts, frontend and the session/WebSocket gateway are
unchanged. Missing APIs still return 503; HTTP 101 parity remains pending.

Run only against a disposable installation or an operator-prepared test copy.
The database and authority must be independent directories, for example
`/tmp/he-preview/live/main.sqlite` and `/tmp/he-preview/authority`. An existing
installation must retain its matching authority and recovery gates. This mode
never repairs or bypasses them. Writable startup may initialize a new database,
keys and authority, load configured plugins, and perform their real work.

Build the command with the native ICU, FTS5 and builtin artifact profile described
by the backend Makefile/native ICU documentation. Runtime preview requires:

- `sqlite_fts5`, `humanengine_icu`, and `bundled_wasm` build tags and their real
  native build inputs. The ordinary Makefile build is not silently promoted.
- The pinned libvips library, passed as an absolute `--image-library` path.
- The pinned sqlite-vec extension passed as an absolute `--vector-extension`
  path, or a binary built with `bundled_sqlite_vec` when that option is omitted.
- An existing `--plugin-root` directory and a resolvable `--node` executable.
  Registration/enabling remains owned by the application. No probe Node process
  starts merely because runtime preview starts. Zero enabled V1 plugins keeps
  the existing lazy-worker contract.
- The installation's `--source-timezone` (default UTC) and `--locale` (en-US).
- `HUMANENGINE_ADMIN_TOKEN` in the environment, never in CLI arguments. Without
  it the existing HTTP authentication boundary returns 503.

Example, with paths to already-provisioned pinned native assets:

```
./humanengine --runtime-preview \
  --database /tmp/he-preview/live/main.sqlite \
  --authority /tmp/he-preview/authority \
  --listen 127.0.0.1:3001 \
  --plugin-root /tmp/he-preview/plugins \
  --image-library /opt/humanengine/lib/libvips-cpp.so.8.18.7 \
  --vector-extension /opt/humanengine/lib/vec0.so
```

The listener is reserved before installation enrollment. Native preflight checks
FTS5, ICU, image integrity/loading, sqlite-vec integrity/loading/version, Node
executable resolution, plugin directory and distributed builtin availability.
No builtin is added or implicitly enabled by the launcher. Application Open
performs its own validation again and owns all business services.

SIGINT, SIGTERM and Serve errors all enter the same real shutdown boundary.
HTTP acceptance stops while the application's admission barrier seals and its
producers and accepted requests physically join. Drain uses contexts independent
of the signal; it has no forced-close timeout. The application releases the
installation only after its owner sequence completes, then flushes logs. Failed
startup may return a partial owner, which is drained through the same API rather
than closed out from underneath its producers. A drain error is a failed exit,
not successful readiness or shutdown. Raw plugin stderr is not forwarded by this
command because it can contain secrets; structured owner diagnostics remain.

The focused command tests use a real subprocess and ephemeral TCP listener. A
100-Continue handshake proves a PUT reached the application's body reader before
SIGTERM. The process must remain alive until that body is completed, commit the
write, emit its real owner-drained receipt, and reopen the same installation on
restart. Additional tests cover failed bind/preflight without enrollment and
mode conflicts/default refusal. These tests require the real native profile;
they do not skip missing dependencies or replace the graph with a health stub.

## Writer ownership acceptance

The combined candidate includes the installation owner's reviewed cross-process
writer lease. Its retained ownership is released only after physical application
drain; existing read-only opens remain readers. The focused suite includes a
strict different-port, same-database/authority subprocess acceptance test and
checks the first application remains usable after rejection. Socket bind
rejection alone is not this protection. The combined launcher suite still needs
execution before this entry can be considered validated; standalone owner test
results are not a substitute for launcher integration results.
