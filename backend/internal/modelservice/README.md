# Model routing and attempt policy

This package executes the completion-routing and provider-attempt policy in
`src/models.ts` in Go. It is an independently usable policy engine with an
injected invocation, clock and shared scheduler. It is **not** a production
provider adapter or a complete replacement for `Router`. Nothing here is wired
into bot startup, settings writes, external-cost authorization or recovery.

## Implemented source behavior

- `model-roles.ts`: `thinker` becomes `replyer`; normalized duplicate routes are
  rejected. Config, provider policy pointers and model capability arrays are
  copied, including copies passed to invocations. Unknown strings retain the
  source helper's behavior; full persisted input validation belongs to the
  existing `contract.Schemas.Parse("ModelConfig", ...)` boundary.
- `Router` constructor / `completeRequest`: source output-cap validation,
  ordered first-match model/provider lookup, missing-model/provider and
  text/tools/vision skips, exact wire-tool-name validation and collision checks.
  Empty/missing routes, no compatible model, canonical role and candidate index
  behavior are preserved. `Request.HasImages` must describe the already expanded
  context, not a raw prompt that has not passed `expandContextMessages`.
- `request-policy.ts`: default 2 retries and 30,000 ms per provider attempt;
  configured ranges 0–5 and 100–300,000 ms; one-based metadata; exponential
  250/500/... ms delays; source's existing 10,000 ms Retry-After/backoff maximum;
  cancellation before/after invocation and retry waiting; operation budget
  calculation and handled-attempt propagation. No new retry or timeout limits.
- `completeRequest` failure handling: nonretryable failures still allow the next
  model. Retryability is the conjunction of attempted-model outcomes. Status is
  retained only if every failure is retryable; the last four private failures
  are retained. Protocol-disabled, `provider_stream_*`, the exact budget-code
  allowlist and the three memory-read admission codes stop fallback. An unknown
  `budget_*` string does not gain terminal status. Invalid provider policy exits
  before logging/fallback, including the source catch-path policy read.
- `stream-progress.ts` / `role-budget.ts`: role time includes queueing and retry
  waits; native provider time begins after admission. Meaningful native streamed
  content stops managed provider and ancestor role timers without stopping
  parent cancellation. Custom and nonstreaming calls cannot disable deadlines.
  A failure after native content is terminal `provider_stream_interrupted`, so
  response-validation failure cannot replay a request that produced content.
- `invokeProtocol` / `scheduledRequest`: every attempt reacquires the real global
  scheduler with canonical role priority and inherited background/learning
  scopes. Custom provider time includes queueing. Custom cancellation races
  inside the scheduler and releases its slot even if the invocation does not
  cooperate. Native operations retain the slot until actual settlement; a role
  timeout can return while that operation still owns the slot. No per-model
  scheduler, rate allowance, persistent queue or restart replay is introduced.

## Invocation contract

`New(config, options)` defaults to `scheduler.ModelScheduler`. Inject one shared
scheduler for a complete engine, never one per scene/model/provider. Clock timers
must enqueue callbacks rather than invoking them inline, as with the scheduler.

`Policy.Run(ctx, request, invocation)` requires an explicit invocation and returns
its validated value. `Invocation` is an asynchronous starter: return promptly,
then call its settlement callback when the operation actually ends. It may settle
synchronously. Duplicate/late settlement cannot replace a result or free another
slot. The invocation receives copied metadata, the bounded context and a
`FirstContent` callback. Call that callback only for native meaningful streamed
text, reasoning or tools, not headers, heartbeat, role-only chunks or a complete
JSON response from a gateway. It does not implement SSE parsing.

Only a direct, core-created `*ModelError` grants retry/fallback classification.
Wrapping a retryable error, copying its message/status, or implementing a similarly
named interface grants nothing. `HTTPFailure` and `BuiltinNetworkFailure` are for
trusted native transport normalization; the latter takes the source's allowlisted
own/own-cause network codes, never arbitrary V1 error text. The V1 SDK's unfinished
`ModelProtocolError` identity must not be reconstructed by name, JSON shape or
`errors.As`. No V1 error-identity parity is claimed by this package.

`Observe` exposes a bounded policy-event projection, not full diagnostics or
billing. Observer panics cannot fail/repeat a provider operation. `Sleep` injection
supports deterministic oracle replay; normal execution uses the clock and caller
cancellation. `ModelError` public text/JSON omit private causes and failure data;
internal callers may inspect a copied failure list.

`ParseRetryAfter` implements numeric seconds, ECMAScript whitespace trimming and
the three HTTP-date forms, including the source two-digit year pivot of 50. An
optional date parser supplies the source JavaScript `Date.parse` behavior for its
broader, implementation-specific grammar/timezone rules. The default Go HTTP-date
parser is not a claim of parity for every nonstandard JavaScript date string.
The oracle fixes timezone to UTC for its HTTP-date cases. Full provider composition
must resolve this parsing boundary rather than silently broadening retries.

## Deliberately unsupported production composition

There is no production HTTP client or plugin adapter in this directory. The only
HTTP invocation is a loopback integration-test helper. A production composition
still needs all of the following source invariants:

1. Full provider configuration, URL/credential handling and protocol registration
   lookup/generation/lifecycle cancellation, in their original ordering.
2. The real `currentBudget` supplier checks both before custom invocation and at
   the original native prepare/start/recheck points; external-cost restore holds,
   per-attempt reservation, request output caps, settlement/failure accounting and
   recovery ownership. A callback that merely returns “authorized” is not a port.
3. Native request serialization, image preparation and source 413 halving/requeue
   with the original retained deadline; prompt cache, measurements/tokenizers,
   native/custom response/tool/usage validation and streaming transport semantics.
4. Production diagnostics, pricing/usage observations and failure context, plus
   embeddings batch orchestration and list-models operations. Those operations
   do not become implemented merely by exposing provider-policy helpers.
5. Assistant ownership for opaque continuation state. The existing
   `plugins.V1Host.CompleteModelProtocol` is the only valid V1 completion bridge.
   Its required visible validator, generation checks and nonserialized
   `ContinuationRef` must remain intact, with release on message removal and
   discarded late results. This policy package does not accept continuation IDs,
   stringify refs, claim/retain refs or manufacture a fake successful adapter.
   Do not pass continuation-owning results into a production invocation until
   that lifetime/discard contract is composed and tested.

These missing pieces are why this package has no startup integration. Passing its
checks establishes the policy slice, not provider execution, cost safety, recovery
readiness or end-to-end migration completion.

## Source-pinned evidence

The oracle uses actual frozen TypeScript from master
`24fe8a773d87f2f09996e259fca46524c2c02928`; its 11 dependency SHA-256 pins are recorded
and checked by both the generator and Go tests. Node 24.22.1 is required.

From the repository root, with the pinned Go toolchain:

```sh
node --import tsx backend/tests/generate-model-policy-fixtures.mjs --check
go test -C backend -race ./internal/modelservice
go vet -C backend ./internal/modelservice
```

The 67 routing traces retain real Router normalization, selection, tool checks,
retry loops, fallback aggregation and flow events; only `completeProtocol` is
replaced by a synthetic invocation at that boundary. The oracle also runs the
actual Router, native request code, protocol registry, global scheduler and SSE
parser for four queue/cancellation lifecycle traces and three stream traces,
without a network connection. Four helper traces verify managed progress timers; two role-budget races verify
that a later parent abort cannot replace an already-settled stage expiry.
Eighteen policy inputs, 80 retry delays, 18 Retry-After inputs and role/duplicate
route cases cover primitive rules and invalid numbers. Every fixture has a Go
replay test; expectations are not copied into a second policy implementation.

Native tests additionally use a real loopback HTTP server for 429/503 retries and
401 fallback; they exercise parent cancellation, retry-wait stage expiration,
provider timeout without retries, nonstream/custom progress rejection, raw/wrapped
V1-like errors, config isolation, scheduler priorities and private-error hygiene.
Only the loopback test performs HTTP. No credentials, real messages, external
providers, paid calls, production storage, commits or pushes are involved.
