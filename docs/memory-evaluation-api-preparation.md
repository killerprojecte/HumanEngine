# Authorized native evaluation preparation

Status: offline adapter/mock preparation after Slice 14; **no real API calls or keys have been used**. The existing CLI still defaults to dry-run and rejects real-run. Only an explicitly invoked trusted host may construct a native evaluation capability. This is not publication/deployment approval.

## Consent and temporary credentials

Confirm the exact HTTPS endpoint, selected model and allowed operations (model discovery and/or synthetic generation), maximum HTTP requests, elapsed time and requested output cap. One user authorization may cover both operations; it need not be split into repeated prompts. Endpoint owners receive the authorization header and synthetic input. Do not send keys to a different host or follow redirects. Model discovery does not establish a model's price, capabilities or output-cap compliance.

`EphemeralEvaluationCredential.fromPipe(fd)` consumes only an explicitly supplied inherited FIFO descriptor at least 3, bounded to 8 KiB; it never searches environment variables, user history, configuration, or regular files. A host may instead supply a byte buffer, which is wiped on construction. No default key or endpoint is embedded. Dispose the capability and terminate the child after use. JavaScript strings cannot promise cryptographic zeroization; no credential is written to source, reports, SQLite, logs or argv by this adapter. The test uses a synthetic pipe fixture only. `fstat` establishes FIFO type, not whether it was created anonymously; anonymous-pipe provenance is the trusted host’s responsibility. The host must provide the pipe without placing the real key in shell history, process arguments or captured command text.

## Two honest admission modes

- `createNativeEvaluationProvider`: strict existing `BudgetService`/`BudgetLedger` admission. Requires reviewed supplier output-cap and input-estimator contracts, complete applicable rates, a positive USD limit, request count and time limit. Missing contracts fail before transport; no heuristic is called exact. The dedicated synthetic Store's strict settings may not exceed the approval. Existing restored-accounting HOLD, unknown/partial attempts and external authority still block.
- `createBoundedNativeEvaluationProvider`: requires `costMode=request_bounded` and `acceptUnknownCostRisk=true`. Enforces persistent request attempts, elapsed deadline and a transmitted native output-cap request. It does **not** guarantee that the supplier honors that cap, has no USD hard ceiling, and never fabricates zero prices/token counts. An optional `maxUsd` is a soft reference in this mode. The first unknown actual cost, failed/ambiguous request or timeout stops the run. Known costs are reported only when the existing usage partition and prices fully support them. Any existing enabled Store budget gate remains in force; the adapter never silently disables it. Use a separate explicitly configured synthetic Store, not production configuration.

Both reuse the existing Router's native transport, response size limit and redirect rejection, with zero retries/fallback and one model. `RouterConfig.nativeMaxOutputTokens` is a request cap only; if an existing budget supplies a smaller cap, the smaller value wins. Errors returned by the adapter are fixed codes without nested provider bodies/headers/causes. Successful predictions redact the exact credential. Do not enable additional diagnostic loggers that record raw headers.

The separate host-owned `EvaluationSessionLedger` is not a production backup table. Keep its file across host/adapter restarts and reuse the approved run ID. Its durable `in_flight`/`unknown` state is never automatically released. Changing a run's approval/model conflicts instead of resetting its allowance. Do not delete/restore this ledger or invent a new run ID to bypass an exhausted or unknown run; new IDs require new authorized work. No credential or prompt body is stored there.

## Clock separation and synthetic scope

Fixture `occurredAt` is payload data only. The session uses real process monotonic elapsed time anchored to host epoch; the existing budget uses its normal real billing clock, with no injected clock. Never run paid transport under a global fake Date/timer or reset a billing window when advancing a fictional conversation date. A timeout/unknown request remains potentially billed across restart and midnight. Manual restoration acknowledgment is not reconciliation.

The adapter accepts only the frozen fixture's exact speaker, mentioned identities, utterance, memory context, instruction, variant and event time. It does not read production chats or let arbitrary prompts enter through `syntheticOnly=true`. This first harness tests small attribution/extraction prompts and scripted MemoryService baselines, **not** the complete autonomous Engine's real-model quality. Wider actual Engine trials require a separately reviewed synthetic harness and allowance.

## Minimal staged live plan (not yet authorized/executed)

1. With the user's approved hosts and temporary credential capabilities, one `/models` metadata request per endpoint, no redirects or retries. Keep only bounded model IDs; omit malformed or credential-containing values. Do not treat an empty/error response as permission to guess models and generate.
2. Select a returned text model explicitly. Record provider/model aliases and actual model name, advertised pricing/currency, usage/cache partition and cap compatibility. If these cannot support strict USD bounds, ask/use the already approved request-bounded risk mode rather than inventing rates.
3. Start with one frozen synthetic case per selected provider; verify text/usage/cost/latency and stop unknown. An unknown first cost is a useful negative result, not free usage or permission to retry.
4. Within remaining approved calls, run the six frozen cases across three baselines (18 calls per model), with no real persons or chats. Keep predictions separate from expected/forbidden annotations. Human review remains pending; no automatic semantic success claim.
5. Only after reviewing that result, allocate a new explicit budget for full synthetic Runtime learning, multi-person plans, read-pause, recall, source edit and cross-scene scenarios. Do not claim this small runner already covers those real-model loops.

Each report must state attempted/successful/unknown requests, measured elapsed time, cost coverage, approval limits, fixture/runner/prompt versions and unmet capabilities. Revoke the temporary provider keys after the authorized test and notify the user; the adapter cannot revoke third-party credentials itself.

## Launch-parameter handoff template (not an executable consent record)

Fill the placeholders from the user's authorization; do not put a key value in this document or a JSON config. A trusted host constructs the programmatic capability only after the consent exists. Keep the CLI dry-run unchanged.

```text
approval_state: pending
synthetic_only: true
providers:
  - alias: ds-test
    endpoint: https://cpa-ds.awa.hk/v1
    model: discover_once_then_select_returned_id
    credential: inherited_fifo_fd_3
  - alias: gitee-test
    endpoint: https://ai.gitee.com/v1
    model: discover_once_then_select_returned_id
    credential: inherited_fifo_fd_4
scope: models_metadata_and_frozen_synthetic_prompts
max_total_http_requests: <approved integer, INCLUDING models metadata requests>
models_requests_reserved: <0, 1 or 2 according to approved endpoints>
generation_calls_remaining: max_total_http_requests - models_requests_reserved
max_elapsed_ms: <approved total wall duration>
per_request_timeout_ms: <approved bound, no more than remaining duration>
requested_max_output_tokens: <approved bound; supplier compliance not assumed>
cost_mode: <strict_usd OR request_bounded>
strict_max_usd: <required only for strict, with reviewed rates/cap/estimator>
accept_unknown_cost_risk: <must explicitly be true for request_bounded>
soft_usd_reference: <optional in bounded mode, NOT a hard cap>
run_id_per_provider: <unique approved ID; reuse after restart, never reset to bypass a stop>
session_ledger: <separate durable test-only SQLite path, no credentials>
production_store: none
platform_adapters: disabled
retry_count: 0
stop_on_unknown: true
```

The host must split the remaining call/time allowance across providers without multiplying the user's total. Native factory limits are per provider/run; they do not automatically allocate a shared two-provider allowance. For a minimal sequential run, reserve the approved metadata calls, then explicitly allocate the remaining budget before constructing each generation capability. If model choice or cost mode remains unresolved, stop after the approved metadata stage rather than guessing. An approval covering both stages can be used once; this template does not require repetitive confirmation prompts.
