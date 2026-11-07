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
