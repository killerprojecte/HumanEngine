# Source-linked experimental operation hints

> Historical slice 7 checkpoint. Later recovery safeguards are implemented; the opt-in operation-trial continuation is documented in [operation trials](memory-operation-trials.md). The original 897-test count below is not the current aggregate.

This opt-in slice separates explicit feedback, policy candidates, mechanical checks, activation, Planner exposure and observable tool use. It does not learn factual truth from response delivery, and synthetic checks do not establish effectiveness or human-like learning quality.

## Flags and live input

`memoryExperienceEnabled`, `memoryExperienceAutoActivateEnabled` and `memoryExperienceHintsEnabled` default to false. The scene learning switch must also be on for automatic intake or model candidate proposals. Turning a feature back on does not replay old messages or automatically activate stored candidates. Existing factual memories, habits and baseline response behavior remain separate.

The native adapter accepts a small documented set of complete explicit commands in a live, available, nonbot message replying to an actual available bot message in the same scene. It stores canonical source/target identifiers, a fixed command code and trusted receipt time, not the whole message. Quotes, thanks, silence, vague feedback and arbitrary model success assessments do not qualify. Repeated handling of the same source/target is idempotent; changing the target does not create an independent generation root. Fixed instruction families are not independent evidence of improvement.

The three fixed actions are exact-person lookup before semantic expansion, checking plan qualifiers, and presenting a supported conclusion before details. Only the last action has the initial automatic activation path: an explicit request scoped to its speaker, scene learning on, all three experiment flags on, and successful mechanical checks. This is honoring a stated preference, not a reward inferred from satisfaction. Other candidates require explicit local validation and administrator activation. Every hint remains lower priority than manual rules, qualified evidence, scope and budget limits.

## Candidate and review lifecycle

Policies have exact versions, semantic hashes, support roots, condition enums, expiry and validation receipts. The initial expiry is seven days, an engineering default rather than a measured optimal forgetting interval. Local fixed holdouts check shape, action/condition compatibility, source lineage separation, fixed templates and bounded expiry. Receipts retain rule/dataset hashes and are labeled `mechanical_only`; clients cannot submit `passed: true` or arbitrary templates.

Authenticated administration can validate, activate, suspend, retire and delete with expected-version checks. A suspended policy must be revalidated before reactivation. An old reply target cannot revoke a subsequently revalidated policy version. Explicit withdrawal of a personal instruction is distinct from an effectiveness counterexample: withdrawal needs the exact policy selected for the bot reply and the same speaker; a counterexample requires an actually observed applied-operation reference. Unknown or partial sends are never success observations.

## Actual runtime path

The Engine receives trusted feedback without calling another model. A candidate can also be organized through `experience.outcomes` followed by `experience.propose`, but only from an exact outcome actually delivered in this turn and matching its existing instruction code. The proposal cannot invent feedback or activate a policy. Receipt packing keeps or omits the entire bounded outcome set; preparation alone grants no learning authority. A no-reply plan may save a policy candidate without sending a message.

Active, matching policies become at most three short, fixed Planner-only hints. They do not become factual evidence, get injected into the participation stage, or directly give the Replyer a second set of instructions. The Planner remains responsible for selecting relevant facts and expression goals. Matching repeated policies do not duplicate the same action/condition hint.

Counters distinguish inclusion in planning context from observable application. Inclusion does not prove the provider consumed the request. Exact-person application requires the delivered exact lookup to precede semantic search, including searches that return no results. Plan checking records a delivered qualified relation read, not agreement, truth or permission. Expression ordering is not declared applied from a send acknowledgement. Real sent message identifiers can link later explicit withdrawals to the exact selected policy. Multiple sent segments do not inflate per-turn exposure counts.

Every selected policy registers a version/source/flag/expiry validation. Review, source withdrawal, deletion or disabling hints invalidates an in-flight use. Ordinary factual-memory reads do not gain policy dependencies merely because a policy exists. Feedback and policy observation failures do not trigger extra sends or autonomous retries.

## Persistence and limitations

The journal, candidates, validation receipts, use links and minimal tombstones persist in SQLite. Recall and deletion remove dependent private records and usage links; tombstones prevent an identical source root from returning. Retained authorized sources do not depend on the recent-message cache still containing the original. Restart does not call a model, revalidate or activate a candidate. Safe stale-backup recovery is a separate mandatory implementation slice; this feature alone does not claim that old copies cannot resurrect deleted data.

The initial native command vocabulary is intentionally narrow. There is no general natural-language reward classifier, automatic programming, global personality adjustment, learned permissions or proof of improved answer quality. Broader independent task outcomes and real-model comparison require separate authorized evaluation. Source availability and withdrawal are authority checks, not semantic entailment checks. Experimental policy storage has bounded read windows; a limited result is not an exhaustive absence claim.

## Offline verification checkpoint

The slice passed 897 synthetic tests, runtime and UI type checks, formatting and production build. The new real Engine/Runtime integration file contains 22 scripted-model tests; these verify contracts and observable operations, not model quality. Browser checks covered review and cancellation, activation, suspension, deletion confirmation/cancellation, scene switching, independent hint settings saved across reload, and clearing policy details on disconnect. No paid model or real platform send was used. Safe stale-backup recovery remains a separate required slice.
