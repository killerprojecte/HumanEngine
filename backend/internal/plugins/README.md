# V1 compatibility host

V1 plugins use one lazy shared Node process and one unloadable worker per enabled
plugin. Worker exit events retire only the matching lease. A dead isolate stops
appearing in the model registry before its failed call settles; live siblings
retain their workers and shared process. Disabling the final lease exits Node.

## Model completion boundary

Use `CompleteModelProtocol`, not `CallModelProtocol(..., "complete", ...)` or a
raw completion callback handle. Generic completion admission is rejected before
encoding caller arguments. `CallModelProtocol` remains available for embeddings
and model listing. All calls pin their registration generation.

`ModelCompletionRequest` separates visible context/messages/tools from
`Previous *ContinuationRef`. The previous reference belongs to the last
assistant message. Neither it nor its original JavaScript value enters JSON or
structured clone. A worker map retains that exact unknown value, including Map,
Date, functions, cycles, symbols, BigInt, primitives and object identity. References
from another host, protocol, model, retired generation, or released assistant
become JavaScript `undefined`, as does a zero reference. Public JSON cannot mint
or restore ownership; reference JSON marshal/unmarshal is explicitly rejected.

The worker serializes only `{text, toolCalls, usage}`. It temporarily retains the
original result object until the Go caller validates the visible JSON using the
required `Validate` function. That function is the model service boundary for
normalization, tool-name/argument checks, usage validation, and existing response
size policy. It executes outside lifecycle locks and receives a copy of the
visible bytes. This compatibility package does not duplicate those model-service
rules or add protective caps. Missing validation is rejected.

After validation, Go checks cancellation and the current lease, then requests a
claim. Only then does the worker read `result.continuation` and retain the opaque
value. Go checks cancellation and the lease again before publishing the typed
result. Invalid, cancelled, stale, failed-claim and abandoned results are released;
late results from callbacks ignoring cancellation cannot create retained state.

`ModelCompletionResult.Visible` and its JSON marshaling expose only the visible
JSON. `Continuation()` returns a separate opaque capability. The assistant owner
must retain that capability while the assistant remains eligible for reuse and
call `Release()` when it discards the assistant. **Release is mandatory: there is
no GC finalizer.** It is idempotent across copied references and safe concurrently.
Release is shared revocation, not reference counting. If several branches retain
the same assistant, a common owner must wait until the last user discards it
before releasing the reference. Do not release it merely when one branch drops
a message.

A continuation can be reused repeatedly; a new completion has independent
ownership even if the plugin returns the same original JavaScript object. Ending
the originating request context does not release an assistant-owned continuation.
Unload, worker crash and host close clear all values belonging to that isolate.
Do not put continuation references into persistent messages, diagnostics, or
visible context/messages/tools.

This is a typed host boundary, not a completed model router. Provider selection,
credentials, scheduling, budget policy, normalized result validation and assistant
ownership wiring remain the model service's responsibilities. The SDK error
identity bridge below preserves authenticated failures without introducing a
retry loop or new timeouts in this host.

## Source-derived V1 SDK error identity

`NewV1HostWithOptions(node, diagnostics, V1HostOptions{ModelProtocolModuleURLs: ...})`
accepts the exact absolute module URLs the deployment knows legacy plugins import
for `src/model-protocols.ts`. It copies that list and does not start Node. A worker
installs synchronous Node `registerHooks` before importing its plugin, maps only
those URLs to `humanengine:v1-model-protocols`, and loads one canonical embedded
SDK instance. Relative imports are resolved against their importing module first;
package resolution may also resolve to an explicitly listed URL. There are no
basename/suffix guesses. Different SDK copies outside those aliases stay different
constructors. An explicit alias can work without any SDK source file on disk.

The embedded module contains **all runtime exports** generated from the actual
pinned `src/model-protocols.ts`: `ModelProtocolError`, `ModelProtocolRegistry` and
`isBuiltinProtocol`. It is not a hand-written class lookalike. Plugin imports and
the worker classifier share the same constructor. `NewV1Host` remains compatible
and supplies no deployment aliases. **Automatic discovery/wiring of SDK URLs for
a real deployment is still unresolved.** This does not silently claim legacy
plugins with arbitrary import paths are supported. Production composition must
provide that mapping; this package neither installs a global Node loader nor
keeps Node running when no V1 plugin is enabled. Node 24.22.1 remains required.

Only synchronous throws, rejected promises and thenable failures during model
adapter execution enter the source-derived `invokeProtocol` classification.
Authentic subclasses qualify exactly as `instanceof` does. A renamed error still
qualifies. Runtime-mutated kind/status fields and repeated getters follow the
source expression order, without re-running constructor validation. An accessor
or coercion that throws during classification does not grant retry. Visible-output
serialization and continuation getters occur after that source catch and cannot
acquire retry permission. Cancellation and retired registrations take precedence.

Worker-to-host-to-Go metadata is private control data, separate from the plugin's
return value. Public `ProtocolInvocationError` text remains `model_protocol_failure`
and its JSON exposes neither metadata nor cause. `ClassifyProtocolFailure` accepts
only the exact transport-created typed invocation error and returns an independent
policy projection. Wrapping it, implementing `As`, setting a name/status, using a
foreign SDK copy or round-tripping error JSON cannot mint identity. The bridge is
for trusted V1 plugins, **not a Node security sandbox**. Arbitrary own network-code
fields are not normalized into native retry permission. Ordinary `Error`
read-admission messages and own/own-cause network-code handling from the broader
`invokeProtocol` catch remain unimplemented custom-invocation normalization. This
SDK identity bridge is not a migration of that entire catch or invocation path.

The source retry decision is retained even for runtime field mutations. Only safe
integer statuses and fixed failure/admission codes leave the worker; out-of-contract
status strings/objects and nonfinite status numbers are omitted and their arbitrary
code text is sanitized to `provider_request_failed`. This is a deliberate public
error sanitization boundary, not a claim that arbitrary mutated JavaScript status
values become Go integer statuses. Numeric Retry-After, including NaN/Infinity,
retains the source delay semantics privately. Nonnumeric delays are ignored as in
`retryDelay`. No new resource caps, retries or timeouts are added.

`modelservice.V1ProtocolFailure` composes this exact private identity into a trusted
`ModelError`; it does not accept arbitrary errors or unwrap causes. A real host +
policy test verifies retry attempts and delays. This is still not a production
provider adapter: provider configuration/credentials, admission/accounting, visible
validation and assistant continuation ownership must be composed by the caller.

Evidence, from the repository root with the pinned toolchains:

```sh
node --import tsx backend/internal/plugins/generate-v1-sdk.mjs --check
go test -C backend -race -p 1 ./internal/plugins ./internal/modelservice
go vet -C backend ./internal/plugins ./internal/modelservice
```

The generator checks source SHA-256 pins, generates the complete SDK and the
actual `invokeProtocol` retry expression, and records synthetic fixtures by running
the **actual TypeScript Router and SDK**. The Go replay covers the same failures
through complete/embeddings/listModels workers, SDK aliases and foreign copies,
getter boundaries and exact getter read order/counts, cancellation, metadata
privacy and source-free SDK loading. The actual repository SDK module URL is also
resolved through the canonical module and compared by constructor identity.

## V1 adapter boundary

`V1HostOptions.AdapterServices` connects trusted V1 transport code to synchronous
native runtime/authority callbacks. `RegisteredAdapters` returns installation-order
metadata with a generation token. `AdapterSend`, `AdapterHistory`,
`AdapterGetMessage`, and `AdapterStatus` require that token, so a reload cannot
redirect a stale call to the replacement transport. The runtime composition owner
must install/remove native registry handles and supply real ingress/edit/recall
and tombstone reads. An absent required service errors rather than inventing an
authority result. `Edit` carries the complete native SourceEventResult JSON,
including ref/previousRef. Media resolution remains scene-scoped and optional.

`generate-v1-adapters.mjs` embeds the actual pinned `installAdapter`,
`untilAbort`, `awaitSendStage`, and diagnostic normalization sources. Scene hashing,
message/history validation and normalization, tombstone filtering, clone/freeze,
receiver binding, synchronous throws and async returns run in that source code.
The only history/message-size restrictions are the existing source product policy.
Factories are collected during setup, installed in order afterwards, started in
order after all installations, and disposed in reverse order before setup cleanups.
The existing source 30-second adapter-start deadline is retained; no new caps or
transport retry loops are introduced.

Worker-to-Go requests use a MessagePort response and one shared wake-up word.
Payloads do not occupy a fixed-size shared buffer. `edit`, `isRecalled`, `media`,
`receive`, `recall`, and the final `beforeTransmit` guard stay synchronous JavaScript
calls, including exception propagation. The Node control loop and Go protocol
reader stay independent while an isolate waits. Disable cancels native callback
contexts, releases the blocked worker, runs cleanup, and truly terminates the
isolate. A cleanup request can only remove its exact old generation.

These service callbacks are a new **native composition contract**, not additional
restrictions on plugin JavaScript: they perform native work and schedule, rather
than await, asynchronous plugin events, as source runtime.ingest does. Native code
must propagate the provided context. Attempting to await an operation in the same
isolate using that context returns `adapter_native_callback_reentry`; this diagnoses
a native circular wait. Ordinary concurrent callers and other isolates are not
rejected. Discarding that context and synchronously waiting on the blocked isolate
violates this internal contract and can deadlock. No plugin callback is made a
Promise in place of a synchronous API, and plugins can still initiate asynchronous
work normally.

A call cancelled before admission yields cancelled-before-send. Once admitted,
interrupted/unloaded/transport-lost sends are unknown, never replayed. Source
validation outside the transport catch remains an error, not an unknown receipt.
An authoritative receipt is normalized by source code. Private outcome failures
remain separate from receipt JSON; model protocol error identity and opaque
continuations continue through their existing independent paths.

Although RuntimeAdapter permits optional `readImage`, source V1 `installAdapter`
does **not** copy it into the installed wrapper. This compatibility bridge preserves
that observable omission: there is no V1 readImage handle. Native transports that
actually support images use their separate native runtime ReadImage contract.

Synthetic evidence:

```sh
node backend/internal/plugins/generate-v1-adapters.mjs --check
node --import tsx backend/internal/plugins/generate-v1-adapter-fixture.mjs --check
go test -C backend -tags sqlite_fts5 -race -p 1 ./internal/plugins
go vet -C backend -tags sqlite_fts5 ./internal/plugins
```

The differential generator invokes the actual TypeScript installAdapter with the
same synthetic transport used by Go integration tests. Tests exercise registration
validation, synchronous ingress/edit/recall/media, receiver binding, async methods,
large reverse responses, send/guard exceptions, invalid and unknown receipts,
history/tombstones, cancellation, pending native callbacks, unload/reload,
cleanup order, and the legacy readImage omission. They use no user data, accounts,
provider endpoints, database contents or source proxy server.

### Adapter cancellation and installation review regressions

Every active callback now carries an AsyncLocalStorage invocation identity through
its awaited work. This includes source history/getMessage normalization after an
await, not only the transport method itself or beforeTransmit. The Go native RPC
context joins that call's context with the plugin lease, and is retired when the
call ends. Cancellation releases only the matching synchronous waiter; late native
responses cannot settle another invocation. Transport receive and its source
validation explicitly keep lease scope, including receives emitted from inside an
outbound callback. Independently scheduled ingress and other active calls are not
cancelled with that outbound call.

The source wrapper is owned as soon as runtime.addAdapter passes it to the bridge,
before fallible native registration. Startup failure disposes it exactly once in
reverse cleanup order, even when a disposer throws, and preserves the original
startup error. Failed/cancelled native installs hide their metadata and remove
callback handles immediately. If an Installed callback has not returned yet,
Stopped is delivered once after it returns so its late completion cannot occur
*after* its own native cleanup. Native composition must use the generation token
when processing that notification. Explicit forced isolate termination retains the
host's existing semantics: arbitrary uncooperative JavaScript cleanup cannot be
guaranteed to finish after a hard kill.

`generate-v1-adapter-lifecycle-fixture.mjs` runs the actual TypeScript source for
call cancellation, subsequent sends, independent ingress, and idempotent disposal.
The additional native registration failure is a new internal fallible boundary;
its rollback coverage is an actual Node/Go integration fixture, not a claim that
the original TypeScript runtime has a native Installed callback.
