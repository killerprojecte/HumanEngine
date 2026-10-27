# Model protocols

HumanEngine supports `openai-chat` (`/chat/completions`) and `openai-responses`
(`/responses`). Provider base URLs include the API prefix, for example
`https://api.openai.com/v1`. Enter provider API keys directly in the UI; they are encrypted in the database, with a separate persisted installation key. Optional legacy environment references remain supported. Empty credentials support local endpoints without authentication. See [credential storage](secrets.md).
No browser response, diagnostic log, or thrown error contains the credential or
an upstream error body. Redirects are rejected so authorization cannot silently
move to another endpoint.

Model instance IDs are independent of API model names. Each role tries only its
explicit ordered, capability-compatible instances. Every new completion starts
from the first configured instance. Each attempt has a bounded timeout; caller
cancellation stops the route immediately. There is no fee limit, automatic
provider discovery, or implicit fallback outside that list.

## Tool loop

```ts
const result = await router.complete('thinker', messages, tools, signal)
messages.push(result.assistant)
for (const call of result.toolCalls) {
  const output = await executeValidatedTool(call, context)
  messages.push({
    role: 'tool',
    toolCallId: call.id,
    content: JSON.stringify(output),
  })
}
```

The router returns complete JSON tool calls but **does not execute them**. The
core validates arguments against the tool schema and checks scope, cancellation,
and send eligibility. Ordinary final assistant text is not an automatic send.
Tool results must be supplied with the matching call ID, including failed or
cancelled operations. Tool names use dots internally; the wire encoding replaces
dots with double underscores. Ambiguous name mappings are rejected.

Both protocols use non-streaming requests initially: no partial argument fragment
can trigger a send. Truncated/incomplete output, unsupported tools, duplicate
call IDs, or malformed argument objects fail the entire attempt. Model fallback
cannot itself replay a tool because this module never executes one. The core
remains responsible for deduplication across later model tool calls and for
providing already-sent segments when rebuilding visible context.

Responses uses `store: false`. It requests encrypted reasoning continuation and
keeps the returned output items in a transient WeakMap associated with the exact
returned `assistant` object. Append that object directly during the tool loop.
It is never serialized as chat history, exposed as a reasoning view, or logged.
The state is reused only for the same model instance. Switching instances or
protocols rebuilds visible text, function calls, and results, not opaque state.

## Embeddings and discovery

`embeddings(modelId, inputs, signal)` explicitly selects one instance and never
falls back to another embedding space. Returned rows are reordered by input
index, and missing rows, non-finite values, and mixed dimensions fail validation.
Each model has an `embeddingBatchSize` setting (default 4, range 1–64), also
editable in the model dialog when the embedding capability is selected. Larger
input lists are sent sequentially, yielding the shared scheduler between batches.
Input order and aggregate usage are preserved across batches. Any failed batch,
cancellation, or dimension change rejects the operation without returning partial
vectors or automatically retrying earlier requests. Successful batches may still
have incurred provider usage, recorded individually in the usage log. An empty
input list performs no request. Protocol plugins use the same batching boundary.

The memory service additionally owns the space identity (provider, model,
dimensions, normalization, preprocessing version) and index rebuilding.

`listModels(providerId, signal)` supports optional name completion; a failed or
unsupported list endpoint must not prohibit manual model names. Model listing
is not a capability test. Configured capabilities still need explicit probing
against each deployed provider/model combination.

## Verification boundary

The focused tests run against a local HTTP fixture: both tool roundtrips,
Responses continuation, independent same-name instances, ordered fallback,
partial-call rejection, sanitized errors, cancellation, timeout, model listing,
and indexed embeddings. These are protocol contract checks, not general evidence
of production provider compatibility or QQ delivery.

A bounded live smoke test on October 26, 2026 used synthetic text with Gitee's
`Qwen3-Embedding-8B` through this router: five inputs were sent as 4 + 1, producing
five 1,024-dimensional vectors and reported aggregate usage of 20 input tokens.
A separate Planner endpoint attempt failed at the transport layer before an HTTP
response; a credential-free reachability check also failed. Planner/Thinker live
tool roundtrips, vision, semantic quality, and QQ delivery remain unverified.
Test credentials were transient and are not part of the repository or saved
configuration. This smoke result does not establish ongoing service availability.

Protocol references checked during implementation:

- [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling)
- [OpenAI reasoning models](https://developers.openai.com/api/docs/guides/reasoning)
- [Create embeddings](https://developers.openai.com/api/reference/resources/embeddings/methods/create)
- [List models](https://developers.openai.com/api/reference/resources/models/methods/list)
