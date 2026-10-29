# Request budget admission experiment

This opt-in slice adds a separate durable admission ledger. It does not establish real-model learning quality, token savings, or human-like memory. When disabled, admission does not write budget reservations or change native request bodies. Existing usage and cache-aware cost statistics continue recording normally; turning admission off does not disable them. Scheduling is deliberately improved independently of admission: explicit foreground retrieval uses foreground priority, while embedding rebuilds use maintenance priority. This is not a promise that queue ordering is unchanged.

## Capability boundary

Strict mode requires a programmatically registered, verified estimator and provider contract. No built-in provider/model name is treated as proof of tokenization accuracy or output-cap compliance. Without these capabilities strict mode refuses requests before network dispatch. A UI checkbox cannot make a provider trustworthy.

Best-effort mode uses serialized UTF-8 request bytes plus 1,024 as an explicitly approximate input reservation. It is not a tokenizer and is not a guaranteed upper bound. It currently supports native OpenAI-compatible Chat and Responses text requests and native embeddings; opaque images, continuation inputs and custom protocols are refused while admission is enabled. Output caps are sent on the actual native request, but best-effort mode does not promise provider compliance.

All limits are administrator-set budgets in USD, token estimates and request counts, not recommended or actual provider prices. Model and cache prices remain configured independently. Missing required prices are not assumed free. Native input price categories include ordinary/read/write and supported write TTL buckets; embeddings reserve ordinary input only.

## Accounting and scheduling

Every physical attempt reserves atomically, including retries, fallback candidates and embedding batches. Runtime turns share an explicit turn identity across model roles. Realtime learning batches and contextual jobs have independent identities; they cannot elevate their lane to foreground. Realtime and maintenance lane allowances together cannot exceed half the daily budget, reserving at least half for foreground work.

Dispatch marks the attempt started. Known pre-dispatch failures can release their reservation; requests already sent with missing or uncertain usage remain held. Partial usage settles only known amounts. Actual usage is never clipped to the estimate. Violations invalidate the corresponding estimate contract; an exposure without a reliable bound is explicitly reported and blocks further strict-mode admissions across models.

The ledger calculates money with fixed-decimal rate arithmetic and rounds the combined amount once to nanoUSD. Historical usage reports remain unchanged; their totals must not be added to this ledger because these are overlapping views, not separate bills.

The read-only budget endpoint distinguishes known, held, partial, unknown and unbounded amounts. Held amounts are not confirmed invoices. Unknown reservations survive process restart and UTC rollover; the service never assumes another boot is dead or silently clears its usage. There is no public blind-release or reconciliation endpoint. Provider-receipt-based reconciliation and verified production estimators require separate trusted integration.

## Validation scope

Offline fixtures exercise native wire caps, per-attempt reservation, immutable pricing snapshots, cancellation, retries, unknown exposure, exact accounting and concurrent SQLite admission. Actual provider compliance, tokenizer calibration and cost/quality tradeoffs require the user-supplied authorized model evaluation later. No paid model calls are part of this slice.
