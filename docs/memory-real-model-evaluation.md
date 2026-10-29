# Real-model evaluation handoff

This is a test plan, **not an authorization to make paid calls**. The implementation
has been exercised with synthetic data, scripted completions, local HTTP providers
and fake transport acknowledgments. Those tests establish deterministic contracts;
they do not measure semantic learning, human-like memory, answer quality or savings.

For the new synthetic evaluation adapter, use the [temporary pipe/session protocol](memory-evaluation-api-preparation.md), not environment or persistent model credentials. The broader installation settings below describe the application, not that adapter. No real endpoint has been called during preparation.

## Before supplying an API

Use a separate test installation and synthetic scenes, with all real platform
adapters disabled. Do not import personal chats to obtain an initial score. Choose
one provider/model and a small explicit spending ceiling; configure its credential
through the supported model settings or the documented environment mechanism, not
in a repository, screenshot, fixture or report. Do not enable diagnostic raw I/O for
real conversations. Clarify the provider's retention policy separately.

Record model/API protocol, settings, prompt/source version, known price snapshot,
output-cap support and tokenizer availability. Unsupported strict budget contracts
must remain refused. Best-effort estimates are not hard guarantees. Restored data
remains subject to model HOLD; manual acknowledgment is not accounting reconciliation.
Keep the original independently retained recovery authority and test rollback using
[operations](operations.md), not by replacing a live SQLite file.

## Staged protocol

1. **Dry run:** confirm default-OFF experiments, no adapter sends, no automatic
   old-chat replay and successful backup/recovery. Verify the chosen runtime path
   cannot call a model before explicit test initiation.
2. **Protocol/accounting smoke test:** one short completion and one small embedding
   only if needed and authorized. Compare returned usage, cache buckets, output cap,
   retries and ledger coverage. Missing usage remains unknown. Stop on unexpected
   calls, spend, credential errors or silent fallback to another provider.
3. **Frozen semantic suite:** use held-out synthetic cases covering explicit self
   facts, third-person reports, quoted text, jokes, negation, hypothetical/future
   statements, unknown subject, corrections, scene separation and noisy bursts.
   Score false durable assertions separately from missed useful assertions.
4. **Multi-turn lifecycle:** two participants confirm an 18:00 proposal, propose
   19:00, independently confirm that exact version, then edit/recall one confirmation.
   Check incomplete current state versus retained historical declarations; never
   score a self-reported completion as externally observed completion.
5. **Retrieval/handoff:** evaluate necessary-evidence recall, distractor resistance,
   preservation of conditions/time/source, omitted-evidence honesty, history versus
   current state and cold-record explicit retrieval. Mechanical source membership
   is not a substitute for checking whether a generated statement follows its evidence.
6. **Controlled experiments:** compare the simple baseline against one feature at a
   time (relations, strategy hints, working set, topic routing). Same cases, model,
   budgets and judging rubric; keep a held-out set. Do not optimize and report on
   the same examples. Candidate/accepted/mechanically validated does not mean useful.

## Record outcomes, not impressions

For each case retain only authorized synthetic identifiers and expected conditions:
pass/fail/uncertain, false-positive writes, necessary-evidence misses, current versus
historical confusion, cancellation/duplicate calls, actual provider usage coverage,
known/unknown cost, serialized bytes and observed latency. Report sample sizes and
raw ranges; a tiny smoke test is not a production p95 or a savings estimate.

Stop immediately on cross-scene exposure, revoked-body use, incorrect authority,
unintended platform transmission or unbounded repeat calls. Isolate the case, retain
a redacted reproducible fixture, fix and rerun the deterministic regression before
resuming paid tests. Semantic misses without a safety failure should remain visible
in the result rather than being relabelled as infrastructure success.

## Not yet demonstrated

No real-provider quality, tokenizer calibration, financial savings, autonomous
semantic consolidation or generalized human-like episodic understanding is claimed.
Limited experience views do not synthesize reality; fixed strategy actions are not
an unconstrained self-modifying policy. See [current status](memory-current-status.md)
for implemented scope and remaining offline work.
