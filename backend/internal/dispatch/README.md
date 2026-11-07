# Guarded single-segment dispatch

This package ports `src/runtime/dispatch.ts`. It performs eligibility/recall checks,
optional typing, asynchronous validation, simulation receipts and one physical
adapter invocation. The final transmit callback rechecks eligibility, the exact
adapter handle, recall, captured topic/source validity and grounding. A receipt
already established by that invocation remains authoritative after cancellation.

There is no persistent queue, restart replay, resend, retry, timeout, resource cap
or fallback adapter. The package is not yet composed into a running Go bot.
Adapter installation, transport/plugin receipt validation, typed V1 bridges,
turn ownership and production diagnostic sink composition remain separate work.

## Invocation and cancellation protocol

Go goroutines are not JavaScript microtasks. A `select` between a result and a
cancelled context would randomly select among two ready cases. This package uses
an explicit protocol instead:

1. The owner creates a `Signal` for the turn and calls `Abort` directly when the
   turn is invalidated. `Signal` implements `context.Context` for ordinary Go IO.
   It retains the first cause. It does not create a deadline or watcher goroutine.
2. Callback admission checks cancellation under the settlement mutex. An already
   cancelled stage does not invoke its callback. Once admitted, the callback is
   invoked exactly once. A concurrent cancellation after admission belongs to
   that invocation, even if it precedes the next goroutine instruction.
3. A synchronous callback returns `Resolved(value)` or `Rejected(cause)`.
   These explicitly mark an inline outcome, which wins over cancellation inside
   that invocation. Panics are captured as synchronous failures. A future already
   settled before admission also has this priority, matching an already settled
   promise returned from the JavaScript callback.
4. Asynchronous callbacks return `NewFuture[T]()` promptly and resolve/reject it
   from the worker that owns the asynchronous operation. Deferred settlement and
   cancellation have a single publication order. An asynchronous result cannot
   acquire inline priority simply because its goroutine ran before the callback
   returned. Do not use `NewFuture` plus `Resolve` inside the invocation to express
   an inline result; use `Resolved` instead. Do not create a `Resolved` future in a
   background worker and return it as though it were an inline result.
5. Synchronous abort listeners finish before cancellation continuations proceed.
   Notifications only wake a waiter; they do not choose its result. The one-slot
   wake channel coalesces wakeups, not receipts or work. Publication and observation
   use short critical sections with no user callbacks, IO or logging under the
   settlement mutex. There is no global bot work queue.
6. Cancellation retires observation even if a callback ignores its signal. Late
   success/rejection and duplicate settlement cannot change the returned receipt.
   Futures do not have unhandled-rejection side effects. Dispatch creates no
   background goroutine that would remain blocked in an uncooperative callback.

Callbacks must return promptly. Adapters own any IO goroutine and must call the
provided final transmit gate immediately before the physical effect, after all
asynchronous encoding/preparation. They must translate a rejected gate into their
own authoritative non-delivery receipt if they can prove non-delivery. A thrown
adapter error after invocation yields `unknown`; dispatch cannot infer that an
error proves the send did not happen.

A core-installed adapter wrapper that already owns cancellation across its full
send scope is marked with `AbortContainedAdapter` before registry publication.
The dispatcher then observes that wrapper's future without an additional abort
race. Such wrappers and typing-delay implementations must actually own their
cancellation and cleanup, matching the original TypeScript boundary.

Registry handles are immutable after publication. Registry lookup, eligibility,
recall, grounding and captured transmit-state functions must synchronize their
own state. Different scenes can dispatch independently. Cancellation uses the
turn's signal and never cancels another scene's invocation.

## Receipts and diagnostics

The receipt preserves absent versus empty optional message IDs/details. Segment
parts are passed through as raw JSON; dispatch does not reinterpret media, impose
new limits or claim a complete lossless JSON-string migration. A simulation uses
a new UUID and does not capture a transmit guard, look up an adapter or send.

Private outcome errors retain the original cause and inner operation/stage
metadata, including primitive cancellation causes. They are excluded from receipt
JSON. Diagnostics receive deterministic stage/status, reason, counts, next step
and error-context fields without arbitrary error messages. A sink failure cannot
change delivery or trigger a retry. The flow projection intentionally excludes
random IDs, duration and full stack/error rendering; it is not a replacement for
the existing complete diagnostic subsystem.

## Executable evidence

From the repository root, using the pinned Go toolchain and Node 24.22.1:

```sh
node --import tsx backend/tests/generate-dispatch-fixtures.mjs --check
go test -C backend -race ./internal/dispatch
go vet -C backend ./internal/dispatch
```

The generator imports the actual TypeScript dispatch implementation and pins its
source, receipt types and relevant diagnostic helpers by SHA-256. It uses no live
messages, database, networking or paid providers. It produces the diagnostic-code
allowlist and 15 stage-settlement plus 79 dispatch traces. Go tests verify the
source hashes and compare complete deterministic call/flow sequences, receipts,
private error metadata, invocation counts and listener cleanup. Coverage includes
same-invocation receipt authority, deferred self-abort, late receipt/rejection,
validation cancellation, before-invoke cancellation, missing/changed adapters,
recall, grounding, topic guards, typing, simulation and optional-field presence.

Additional Go tests exercise a real local HTTP effect cancelled after server
acceptance (one attempt, uncertain receipt, no replay), concurrent independent
scenes, deferred completion before invocation return, the synchronous abort
listener fence, one-shot future settlement, private error JSON isolation and
broken diagnostic sinks. These focused tests are not full backend, transport,
plugin, production-bot or performance acceptance.
