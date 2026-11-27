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

All four guest binaries are generated from source with the Go toolchain in
`backend/go.mod`. Their `dist/` directories and generated artifact checksums
are not source-controlled. A published artifact's checksum belongs with the
release, not in the source tree.

    make -C backend wasm

The build targets WASI preview 1 and embeds its guests only for the tagged
bundle. The bundled/source-free tests execute the linked guest bytes and check
the manifest IDs and API version.

Builtin source and resources are grouped under backend/plugins/<plugin-id>/.
Each directory owns its manifest.json, core/ business code and fixtures, wasm/
entry point, and tagged dist/<plugin-id>.wasm embedding. The metadata package
only aggregates resources; it does not own manifests or generated binaries.
SnowLuma-specific native facilities belong under its host/ directory.

Default builds do not require generated dist files. Run make wasm before a
bundled_wasm build; make clean-wasm removes the four generated artifacts.
make plugin-layout-check rejects former split directories and stale references.

### External V2 send-delay packages

The same host hook supports the bundled typing-delay policy and independent V2
packages declaring `["send-delay", "schema-form"]`. The plugin owns the policy
and configuration schema; the host owns the timer and its cancellation. An
external package does not start until enabled, cannot install another kind of
hook through this capability, and cannot declare credential fields. There is
one active send-delay hook per runtime.

Build the independent package outside source control:

    make -C backend wasm-typing-delay-package TYPING_DELAY_PACKAGE_DIR=/tmp/typing-delay-package

With a Go host built without `bundled_wasm` (the opt-in Docker build arg
`PREVIEW_PLUGIN_PROFILE=external-only`), mount that directory at the plugin
root and include `typing-delay.v2.json` in `HUMANENGINE_PLUGINS`. Enable it in
the plugin settings and adjust speedFactor as desired. The bundled compatibility
profile still pre-registers the original package; do not list both under the same ID.

A V2 send-delay guest is called once per complete outgoing segment with
`{"operation":"delay","segment":{...},"config":{...}}` and returns an integer
`{"delayMs":123}` or an error. Disabling the guest or invalidating the turn cancels
pending timers. The generic host uses the existing physical work admission,
exact generation predicate and instance-bound call, without adding a worker or
a second work registry.

### External public webpage GET preview

`make -C backend wasm-web-read-get-package WEB_READ_GET_PACKAGE_DIR=/tmp/web-read`
builds an independent, default-disabled `web-read.v2.json` and WASM pair.
Install the manifest via the ordinary external plugin mechanism; the
`external-only` preview image embeds neither this plugin nor the old candidate.

This scoped package exposes only `web-read.read`. Native code owns URL and
destination admission, DNS pinning, HTTP GET framing, redirects and physical
socket lifetime. The guest interprets the inert preview; Planner sees
extracted text and a cache reference, not a duplicate of the raw preview.
`tool-results.read` can read the complete original entity by byte range without
another GET. Structured requests, sends, and search providers are not part of
this package. The default compatibility distribution is unchanged.

### External public structured HTTP preview

Build the opt-in package with
`make -C backend wasm-web-read-request-package WEB_READ_REQUEST_PACKAGE_DIR=/tmp/web-request`.
Install its manifest and guest together and name the manifest in
`HUMANENGINE_PLUGINS`. This is an alternative manifest for the same
`web-read` plugin ID, not a second copy to install alongside the GET-only
profile. It stays off until enabled, and does not change distro defaults.

This profile adds the read-only `web-read.request` for public GET, HEAD and
OPTIONS, including explicit request headers, redirect settings, and optional
UTF-8 or base64 bodies on GET and OPTIONS. The host owns address and framing
policy and does not allow a method capable of sending a planned action.
Responses retain the HTTP status, raw header order and duplicate names, and
exact entity bytes without content decoding. The model sees a bounded preview;
`tool-results.read` reads later byte ranges of the same complete cache without
refetching. HEAD returns an empty entity. The separate staged
`web-read.send` remains outside this profile.

To switch between these profiles, stop the Go backend, replace the complete
package directory, and restart with the same database and authority directory.
Mount only one package under the plugin root and leave its manifest entry
unchanged. To go back, repeat the process with the original package. The
manifest is loaded at startup; toggling enablement does not reload it. Both
profiles can have the same version and WASM digest, so identify the installed
profile by its manifest, not by the guest digest alone.

### External Bing HTML search preview

Build the opt-in package with
`make -C backend wasm-web-search-bing-package WEB_SEARCH_BING_PACKAGE_DIR=/tmp/web-search-bing`.
Install its manifest and WASM together, as an alternative to the Tavily
manifest for the same `web-search` ID. Select `provider: bing` explicitly and
enable `allowExperimentalHtml`; a different fallback provider keeps the
package inactive. No default or saved provider selection is rewritten. The
profile preserves the saved Tavily credential for rollback but never loads it
for a Bing request.

The active profile makes a fixed public HTTPS GET with only the search query.
It parses inert HTML, rejects challenge pages, and decodes supported citation
wrappers without visiting the linked site. Empty results, an unavailable page,
and a partial parse remain distinct. The complete response is separately
available by range through the turn cache when caching succeeds. This is a
best-effort public HTML preview, not a search API or a live-access guarantee.
