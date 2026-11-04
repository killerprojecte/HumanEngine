# Model usage time ranges

The model usage page defaults to the last 24 elapsed hours. Today and yesterday use the browser's calendar timezone, visibly named beside the picker. Seven/fourteen days preserve the local wall-clock time; one month is a calendar month with end-of-month clamping. Custom local date/time inputs require an ordered pair; nonexistent local times in daylight-saving gaps are rejected rather than silently shifted. Requests convert boundaries to explicit UTC instants and select `[start, end)`.

`GET /api/usage` keeps its existing all-time daily response when no query is supplied. The optional paired `start` and `end` ISO timestamps filter on completion/accounting time. `bucket=hour` groups precise rows into UTC hours; default/day groups into UTC dates. Every counter and estimated cost uses the same selection. The page uses hourly charts for up to 48 hours and daily charts for longer selections. The detail table and chart explicitly label UTC buckets; the applied UTC interval is displayed to avoid ambiguity around local day boundaries and daylight-saving changes.

## Historical limits and retention

Pre-upgrade data has only UTC daily aggregates. On the first upgrade, those aggregates are copied once into `model_usage_history`. New calls atomically update both the existing all-time aggregates and a timestamped accounting ledger. Range queries union disjoint historical and precise data, never the all-time table with its own details. A full UTC historical day is included only if the requested range contains the entire day. Historical daily data cannot supply hourly points.

The `X-Usage-Incomplete: true` response header identifies excluded historical data that overlaps the requested interval. The page displays an explicit incomplete-range warning. Missing historical boundary data is never proportionally estimated or presented as zero. The hourly chart may be incomplete even when its daily cost table has enough historical information.

On the first recorded call of a UTC day, records before the UTC midnight 90 days ago are transactionally compacted into historical daily totals, then removed from the detail ledger. The all-time table is unchanged. This bounds detailed retention by time (90–91 days), not by call count; daily summaries remain available. Idle stores compact on their next call. A timestamp index supports range reads and retention scans; historical reads use the day-leading index. The ledger contains only numeric usage/cost counters and model/role identifiers, no prompt or response content.

## Token and cache semantics

Input and output are separate provider-reported quantities. Cache-read and cache-write tokens are input subsets, not extra tokens to add to total input. Cache miss is not cache write. The page retains per-counter known-call coverage and shows unknown quantities as unknown rather than treating absent counters as zero. A displayed subtotal with coverage less than all calls is partial.

Cache hit rate is `sum(cacheReadInputTokens) / sum(inputTokens)`. It is shown only when every included call has known input usage and a reported cache-read counter, and the denominator is nonzero. This is token-weighted, not an average of per-request percentages. Estimated costs retain the existing price-snapshot and incomplete-price semantics. Request/prompt similarity is not used as evidence of a provider cache hit.

Reference comparison: [MaiBot's statistics service](https://github.com/Mai-with-u/MaiBot/blob/c6429547af711555e3d4d1b8d04b413572cb616c/src/services/statistics_service.py) aggregates cache hits and misses and uses `sum(hit)/(sum(hit)+sum(miss))`, returning no rate when no eligible tokens exist. Its dashboard separates input/output from cache metrics. HumanEngine uses its normalized input-inclusive provider counters and additionally exposes reported cache-write input.
