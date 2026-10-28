# Cache usage conventions (verified 2026-10-28)

Provider counters are normalized to an inclusive `inputTokens` total. Configured prices are separate; no price or multiplier is inferred from these documents.

- [OpenAI Chat Completions usage](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create): `prompt_tokens_details.cached_tokens` and optional `cache_write_tokens` describe subsets of prompt input. The write field is described as “The unadjusted number of prompt tokens written to cache.”
- [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching): Responses `input_tokens_details` includes read/write counters and `cache_write_tokens_details.ttl_30m_tokens` / `ttl_12h_tokens`. Its accounting example subtracts read and write from total input to obtain ordinary input. Those TTLs must not be relabelled as Anthropic's 5m/1h buckets. Earlier model generations have different write-charge semantics; arbitrary configured model aliases are not a reliable generation identifier.
- [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching): raw `input_tokens` excludes cache reads and writes. Inclusive input is the sum of ordinary input, `cache_read_input_tokens`, and `cache_creation_input_tokens`. `cache_creation.ephemeral_5m_input_tokens` and `ephemeral_1h_input_tokens` sum to aggregate creation. Custom adapters can call `normalizeProviderUsage(raw, 'anthropic')`; normalized custom-protocol `Usage` is already inclusive and is never added again.
- [DeepSeek Chat Completions usage](https://api-docs.deepseek.com/api/create-chat-completion/): `prompt_cache_hit_tokens` equals nested cached tokens, and `prompt_cache_miss_tokens` is input not served from cache. Misses are ordinary input, not a separate cache-write bucket. A consistent hit/miss pair establishes a complete read/ordinary partition.

- [OpenAI embeddings usage](https://developers.openai.com/api/reference/resources/embeddings/methods/create): the native endpoint defines only prompt and total counts, with no cache billing fields. In explicit native embedding mode, valid prompt-only usage maps to ordinary input with zero read/write buckets. Explicit cache metadata, if supplied by a compatible endpoint, is parsed rather than overridden. Custom normalized embedding usage remains explicit and is not inferred from this native convention.

## Unknowns and validation

An omitted category is unreported, not reported zero. `cacheKnown: true` requires a complete, consistent input partition with explicit numeric read/write counts and derived or supplied ordinary input. OpenAI read-only metadata without a write count remains partial: current Chat documentation also exposes optional writes and does not state that omission universally means zero. No provider/model-name guessing fills that gap. Explicit zero counters are preserved.

Invalid or contradictory native counters mark usage unknown; valid raw counters are retained only in a fixed numeric-key allowlist. Custom normalized usages reject malformed counts, inconsistent partitions and arbitrary raw fields. No response text, URLs, credentials or arbitrary provider metadata are retained. Embedding batches retain aggregate category values only when every batch reports that category; actual per-request accounting remains per batch.

`ModelLog.pricing` snapshots the actual selected model's configured prices and API model name from the Router's cloned configuration. A snapshot with unset prices is authoritative: later configuration changes must not retroactively price that request.

## Estimates and migration

Rates are explicitly configured in USD per million tokens. No default rates, model-name
heuristics, automatic multipliers or currency conversion are applied. A reported TTL
uses its own configured write rate; the unspecified-TTL write rate prices only the
remaining, unclassified write tokens. Read, write and ordinary input are disjoint.

A missing counter or price is unknown, not free. When only some validated categories
can be priced, the API and interface show a **known subtotal**, with separate fully
and partially priced call counts. For example, known cache-read and output charges
can be counted while ordinary input and unreported writes remain unpriced. Independently
reported read/write token counts retain their own coverage counts. Contradictory
usage is not used for partial estimates. Explicit zero prices remain meaningful.

SQLite migration adds columns transactionally and preserves old call counts and
estimated totals. Old rows are labelled legacy: their missing cache breakdown cannot
be reconstructed, and changing prices does not retroactively reprice them. Category
cost subtotals plus the legacy estimate equal the displayed known total. Role aliases
continue to aggregate under Replyer without rewriting historical role rows.

Tests use synthetic counters, temporary databases and local fake HTTP responses;
they do not call paid providers or establish actual provider billing accuracy.
