# Native plugin management checkpoint

This slice adds the native plugin manager and authenticated management HTTP
routes. It is a tested checkpoint, not a claim that the full application and
all four bundled plugins are complete.

## Ownership and behavior

The application supplies the existing V1 Bridge host, native Runtime, database
and recovery authority. The manager does not create a second runtime host.
Configuration validates before the writer transaction; preparation and secret
mutations are committed with the configuration. Each explicit operation retains
one module across first validation, queued validation and synchronous preparation.
Enabled reconfiguration preserves module state while replacing runtime activation
capabilities. Runtime tools, actions, hooks and events admit only their exact
activation generation.

Explicit disabled configuration may temporarily own a Node control process and
promote that module if Enable is accepted during the operation. No enabled V1
plugin or explicit configuration work leaves no persistent Node process.
Disabling removes registrations and waits for actual native callback work and
worker/process exit. Unexpected exits revoke only the matching generation.
A later independent configuration of a fully unloaded disabled plugin imports a
fresh module: arbitrary module-local state across such separate operations is a
known compatibility difference, not an assertion of full equivalence.

The configuration routes preserve secret redaction and secret update/clear intent.
Startup and configuration errors retain request IDs and source HTTP error shapes.
More detailed structured, secret-redacted startup causes remain follow-up work.

Tool results stream through the native cache. Cache ownership captures source
snapshots, dependencies, explicit extension capture guards, and already-adopted
cache guards. It does not copy all ordinary turn validations. The source engine
does not have a separate Meme asset/version exposure owner: memes.search's
ordinary validations must not become an extra cross-turn cache restriction.
Application composition of the separate model-read and other source exposure
owners is outside this slice.

Only typing-delay has a composed builtin runtime here. Its callback binds an
actual WASM instance so an old callback or cancellation cannot enter or close a
replacement instance. web-read, web-search and SnowLuma runtime compositions remain
incomplete. No private network/HTTP101 behavior change is included.

## Local evidence and remaining validation

Actual source generators run without source-root overrides and are wired into
the Makefile differential target. They check configuration state trace 6,
enable/configure ordering trace 3, and asynchronous setup trace 7.

On the final production implementation:
- Exact V1 callback-generation admission and already-cancelled tool calls were
  tested with real Node workers. Cancelled calls did not advance the host protocol
  request sequence.
- A retained ordinary tool executor could not enter a replacement worker after
  actual process.exit; new tools remained usable. Unexpected exit revoked native
  registrations and explicit re-enable restored the worker.
- Exact SDK alias inspection and a late old setup failure not aborting a newer
  activation passed in the same focused run.
- The shared-Bridge integration passed with real model calls, an ordinary plugin
  tool, streamed cacheResult, retrieval of the same result ID and bytes, native
  source/SQL owners, dispatch, disable and re-enable. Its HTTP routing uses
  ServeHTTP/Recorder.
- A separate real TCP server/client test passed configuration, enable, action,
  worker-exit recovery, disable and cancelled disabled configuration cleanup.
  It is separate from the shared-Bridge/model/cache integration.
- A freshly compiled real typing-delay WASM guest passed old-bound-callback
  rejection, old cancellation not closing the replacement, successful replacement
  invocation returning the actual delay, and final unload.
- Vet passed for pluginconfig, pluginmanager, plugins, botruntime, toolresults,
  credentials, httpapi, v1adapterruntime and memeengine. The CLI built successfully.
  These checks used the recovered Go 1.27.3 toolchain and native ICU build tags.

Earlier checkpoints separately passed configuration retirement/promotion traces,
physical callback drain, delayed adapter start/registration failure, per-listener
recall, hook identity, streaming and typing-delay timer cancellation. These are
historical focused evidence, not a rerun of every case on this final patch.

The complete nine-package race run was not performed locally on this final slice.
Full default/native CI and complete application composition remain pending.

## WASM build products

No compiled WASM binary is committed by this slice. The existing project target
builds the four guest artifacts from their sources:

    make -C backend wasm

The typing guest source is backend/plugins/typing-delay/main.go with its policy
in backend/plugins/typingdelay/policy.go. The existing target uses GOOS=wasip1,
GOARCH=wasm and go build -trimpath -buildmode=c-shared -ldflags='-s -w'.
The focused guest test adds -buildvcs=false for an independent archive workspace.
The existing bundled/source-free checks inspect manifest IDs and API version 2,
not a pinned typing binary hash; make wasm regenerates all four before bundling.
Those manifest checks alone do not prove the unfinished three runtime compositions.
