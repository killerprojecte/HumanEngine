# Embedding query narrowing

`indexableByIds` retains global lifecycle refresh and exactly the original indexability
rules. It deliberately does not substitute list's temporal/current-applicability or
source-sanitization rules. Candidate IDs drive primary-key lookup without a cap, and
repository row ordering is retained.

Rebuild post-provider validation and both single-item indexing boundaries now read
only their known IDs. Model-read guards, transaction boundaries, content hash checks,
owner/configuration fences, and active-generation compare-and-swap remain unchanged.

Pending calculation still computes the full eligible set and exact total/processed
counts. One eligible-ID-driven join replaces the whole-job checkpoint materialization
and per-record vector-existence queries. Missing vectors, changed hashes and absent
space IDs remain pending. Unrelated historical checkpoints are not materialized.

Tests add 10,000 unrelated checkpoints with three eligible records, query plans for
both composite-key joins, exact counts, stale hashes, missing vectors and absent spaces.
Existing rebuild tests cover callback-era edit/deletion/recall, failed work, explicit
resume/retry, restart ownership, and active-generation races with synthetic providers.

This is not a fully bounded paging redesign: full eligibility/global support refresh
and exact status/count queries remain global. No background work, provider calls,
restart replay, or automatic retry behavior is introduced by this change.
