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
ownership wiring remain the model service's responsibilities. The full SDK
`ModelProtocolError` retry-error contract is **not implemented** here: transport
and plugin errors remain sanitized `ProtocolInvocationError` failures and grant
no retry eligibility. No retries or new timeouts are introduced.
