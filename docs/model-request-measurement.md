# Complete request measurement

Native Chat, Responses and embedding requests serialize their final request body once,
after existing budget output-cap adjustments. The same string is measured and passed to
`fetch`. Measurements include all encoded system messages, message history, tool schemas,
tool results and protocol continuation content in that request. Every retry is measured;
later tool rounds are new complete requests. Embedding batch logs contain disjoint totals.

`ModelLog.requestMeasurement` exposes only aggregate counts and bounded tokenizer contract
identifiers. No request text, credential, URL or response body is stored in this summary.
UTF-8 body bytes are not HTTP framing bytes and are not tokens. Custom adapters control
serialization outside this boundary, so their wire bytes and token coverage remain unknown.

The optional eighth `Router` argument is a server-code-only `TargetTokenizerRegistry`.
Registration must match provider ID, protocol, API model name and codec ID exactly. Native
codec IDs are `native-json:chat/completions:v1`, `native-json:responses:v1` and
`native-json:embeddings:v1`. There is no UI/config registration, downloaded tokenizer,
default model-name guess or network lookup. A registration counts the supplied serialized
request **text**, not the provider's internally framed input. Even a valid counter reports
`providerTokenCount: unknown`, `providerFraming: uncalibrated`, `hardTokenCap: false`.
Offline fixture counters establish the integration path, not production token calibration.
Provider framing/usage calibration requires separately authorized real-provider evaluation.

Unregistered, throwing or invalid counters fall back to exact body bytes and unknown text
tokens. Safe-integer aggregation overflow is null/unknown, not a fabricated exact number.
Unknown zero subtotals must be interpreted with the coverage and counted-attempt fields.
The registry/counter runs before the existing final native budget start/restore-HOLD guard.
No external measurement callback runs between that guard and `fetch`. This telemetry does
not change budget reservation, retries, limits, provider accounting or hard-cap eligibility.

The managed `Engine` exposes the same registry through its trusted constructor option
`tokenizerRegistry`. `Store.recordModel` retains a strict, body-free summary in
`model_request_measured` records even inside flow spans; the logs API exposes its
coverage alongside counts. This does not replace existing usage/cost settlement.
Turning budget admission OFF does not disable usage accounting or these measurements.
