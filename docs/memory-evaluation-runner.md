# Frozen synthetic memory evaluation runner

## Safe default

Run `node --import tsx scripts/memory-evaluation.ts --dry-run` from the repository.
The default (no argument) is identical. JSON goes to stdout; redirect it to a private
local artifact if needed. The CLI reads no provider credentials, has no network
adapter, and refuses `--real-run` or any other argument. It does not use configured
production models, chat history, platform sends or persisted databases.

`memory-evaluation-v2`, seed `20261029`, and the SHA-256 of the canonical fixture,
timeline and variant JSON identify the suite. There is no stochastic generator;
the seed is a declared suite identifier, not a claim that remote models are
reproducible. Source/prompt code must also be pinned to the tested commit. Six
synthetic cases mirror the existing semantic-scenario specifications; they are
**not independently reviewed gold labels**. Do not tune on these cases and claim
held-out quality.

## Executed coverage and ablations

Every variant uses a fresh production `Store(':memory:')` and `MemoryService`:

- `no-memory`: no stored assertions or retrieved context.
- `ordinary-lexical-rag`: scripted qualified assertion creation, Chinese lexical
  retrieval, explicit correction, scene isolation and source recall.
- `historical-read-on`: identical ordinary baseline with the actual
  `memoryHistoricalReadEnabled` setting and `historicalQuery` service enabled;
  the ordinary baseline exercises its disabled gate. Retained prior versions are
  labelled previously recorded, not current truth.

The semantic request uses the authorized context snapshot **after correction and
before the later recall** (2026-10-02). The separate longitudinal mechanical trace
then recalls that source and verifies that it is unavailable for current reads.
These are scripted administrative mutations, not model extraction, autonomous
learning, full Engine turn execution or a demonstration of human-like memory.
Graph, strategy, working-set and topic ablations remain **not covered**; this runner
neither enables nonexistent experiments nor claims their benefits.

Each case records prediction (null in dry-run), expected/forbidden specifications,
mechanical failures and a separate semantic status. Semantic status remains
`human_review_pending`, including when an injected fake model returns the exact
expected text. No model grades itself, no regex declares a joke true/false, and
no mechanical pass is labelled semantic accuracy. Mechanism-only cases explicitly
say `not_a_semantic_evaluation`. Unknown costs remain unknown, not free.

## Future explicitly authorized model execution

The programmatic `runMemoryEvaluation({mode:'real-run', provider, authorization})`
accepts a **trusted host-supplied adapter**, not a URL or credential. No real provider
adapter is selected by the CLI and no real provider was called. A separate opt-in native adapter is now available; see [API preparation](memory-evaluation-api-preparation.md). A host must obtain the user's
explicit provider/model, synthetic-data and budget authorization before constructing
that capability. `confirmed:true` is an integration precondition, not an authorization
UI or proof that consent happened. Never let an untrusted model construct it.

The authorization pins fixture hash, provider identity, maximum calls and timeout. Default `strict_usd` also requires a USD ceiling. Explicit `request_bounded` requires `acceptUnknownCostRisk:true`, permits an unknown upper price and labels `hardUsdCap:false`; an optional USD amount is only a soft reference. Both modes stop on the first unknown actual cost. The following upper-bound rules apply to strict mode. Before every call the adapter must provide a trustworthy upper cost
bound covering any internal retries. The runner refuses a request exceeding the
remaining ceiling, stops on unknown/invalid actual cost, and never auto-retries a
failed or ambiguous call. An adapter unable to attest that contract must not be
used for strict-budget evaluation. A violated cost contract is reported as such;
the runner cannot undo provider spend. Timeout aborts and stops the run; a provider
ignoring cancellation may still finish remotely, so its outcome/cost stays unknown
and there is no automatic replacement call.

Provider exceptions are replaced with fixed error codes; endpoint credentials,
configuration and raw exceptions are not serialized. Predictions are bounded text
and remain untrusted data, not HTML. The adapter must not embed secrets in returned
prediction text: the runner cannot infer whether arbitrary generated words are a
secret. Use only these synthetic inputs and inspect artifacts before sharing.
The report's known-cost subtotal does not include unknown outcomes. There are no
latency percentiles, semantic percentages or token-savings claims.

For future independent human review, retain the immutable raw prediction, record
reviewer identity/version and separate false durable assertions, missed qualifiers,
wrong subjects, current/history confusion and uncertainty. Do not rewrite failed
predictions to match expectations. See `memory-real-model-evaluation.md` for the
broader real-model approval and staged safety protocol.

## Attribution and report provenance (v2)

Every semantic request includes its synthetic speaker ID/name and only the roster
entries explicitly named in the utterance. The roster is fixture data, not an
inferred subject assignment. Human-readable semantic case labels and expected/
forbidden answers are never sent to the provider; request IDs are opaque numbered
cases. Speaker identification does not by itself establish semantic correctness.

Reports pin `runner-v3` and `attribution-extraction-v2`, and for authorized programmatic
runs record the provider/model display IDs and snapshotted call/USD/timeout limits.
Display IDs allow only bounded ASCII letters/digits/dot/underscore/hyphen and reject
URLs and recognizable credential prefixes. Hosts must still supply nonsecret display
aliases; syntax validation cannot determine whether an arbitrary string is a secret.
No endpoint or credential field is accepted. Each attempted model call reports local
elapsed milliseconds when observed (including failure/timeout); dry-run latency stays
null. These tiny measurements are not production latency benchmarks.

The v2 fixture hash (including the explicit participant roster) is
`3b1fe8994cefd505f0aba47540cab84b294969af41fabe3e8c35bb018b06aa2c`.
This replaces v1's hash; old authorization cannot silently cover the changed fixture.
The suite remains six semantic specifications across three variants, plus three
scripted mechanism traces—not 200 scenarios or a full Engine/RAG quality evaluation.
