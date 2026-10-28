# Planner, Replyer and memory

Planner is the decision and research module. Replyer is the language module.
Planner selects relevant conversation, retrieves memory when useful, chooses facts,
constraints, expression goals and assets, and proposes guarded actions. Replyer
phrases the result naturally; it does not repeat research or execute planning tools.

## Memory and learning

Existing authorized memories and manually added memories remain readable with scene
learning disabled. The learning flag controls automatic collection from new chat;
it is not a retrieval switch. Manual administrator creation remains available.
Simulation cannot inspect private real-scene memories or write automatic learning.
Missing/unavailable embedding configuration falls back to bounded lexical search.
Chinese lexical fallback now segments meaningful words rather than treating an
entire Chinese question as one token. This is not a guarantee of semantic recall.

Planner must choose a relevant query; all memories are not automatically injected.
Its `memorySelections` pairs a current receipt ID with exact returned memory IDs.
Membership, scene scope and source validity remain enforced. Replyer receives only
those selected excerpts, with uncertainty and original dates preserved—not entire
search results, confidence scores, source storage metadata or unrelated records.
Logs distinguish retrieval from actual selection for the language handoff.

## Reply suggestions

The **Reply suggestions** setting defaults to **off**, including older settings
that have no field. It does not enable learning.

- Off: the planning tool schema excludes `replySuggestion`; a supplied field is
  rejected at the runtime boundary. Planner must not hide a draft in strategy,
  facts, constraints or expression goals. Replyer chooses the wording.
- On: Planner may include a bounded, optional `replySuggestion`. It is not evidence
  or a required answer, and Replyer may rewrite it entirely.

`expressionGoals` replaces the old required `draftGoals` in ephemeral model plans;
plans are not persisted configuration. Planner no longer receives an instruction
to plan the exact response. Handoffs omit raw action arguments and unnecessary tool
payloads while preserving successful/failed outcomes, short grounding references,
related utterances and their times, the current clock/timezone, and relevant image
uncertainty/frame/budget caveats. Internal reasoning is neither requested nor exposed.

## Role compatibility

The canonical model role is now `replyer`. Legacy `thinker` input is accepted and
normalized without changing model order, provider choices or timeouts. Existing
SQLite/backups do not require a destructive migration; explicit later saves use
the canonical role. Configurations containing both names for the same role are
rejected instead of silently discarding one. Historical usage is aggregated under
Replyer without rewriting old records, and old log role names remain readable.

Synthetic fake-model tests verify permissions, selection, wire contracts and real
runtime delivery guards, not the natural-language quality of an external model.
