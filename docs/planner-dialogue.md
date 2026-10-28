# Planner and Dialogue handoff

## Independently configured responsibilities

The existing `planner` route owns participation, reasoning, research and response planning. The existing `thinker` route is labelled Dialogue and owns quick, natural composition from that plan. Configure each ordered model route independently in Models; both need text and tool-call capabilities. A useful selection criterion is reliable structured planning/tool use for Planner and responsive instruction-following composition for Dialogue. These are requirements, not a verified recommendation for a particular model. No new model, credential, paid call or routing choice is introduced automatically.

Participation remains a small first Planner call, preserving passive/private and proactive/group policy and topic assignment. Image attribution and manual/learned context are prepared before research. Research uses core scoped history, memory lookup, meme lookup, rules, and explicitly declared trusted `effect: read` plugin tools. The built-in `web-search.search` participates only when its plugin is enabled and configured; it retains its provider settings, output attribution, untrusted-content markers and cancellation checks. Unknown-effect tools are not inferred safe.

## Validated public plan

`reply.plan` is a strict terminal tool call, alone in its completion. It contains:

- `action`: `reply` or `noReply`.
- `targetMessageIds` and `contextMessageIds`: supplied message identifiers, with explicitly retrieved available core history allowed as additional sources.
- `strategy`, `constraints`, `draftGoals`: bounded public composition instructions, not internal reasoning.
- `facts`: necessary fact text with nonempty `evidenceIds` references to actual input or tool receipts.
- `assets`: meme asset IDs bound to the exact `memes.search` receipt that returned them.
- `emoji`: selected expression choices.
- `actions`: at most four proposed feedback/learning operations from the explicitly available action tools.
- `deliveryTool`: one core or declared `effect: send` delivery tool (default `messages.send`).

Evidence identifiers prove source availability, not semantic truth. Existing grounding checks remain bounded overclaim/feedback guards, not a universal fact checker. Raw retrieved material is untrusted data. Plan fields do not establish that feedback or delivery succeeded.

After validating the entire plan, the runtime executes proposed feedback/learning actions sequentially through their existing live-source, simulation and persistence guards. Duplicate action requests are rejected before effects. Original action receipts, not planner claims, reach Dialogue and the authoritative feedback renderer. A changed topic blocks pending planned mutations. `noReply`, invalid plans, missing plans, unknown evidence or unavailable Planner fail closed without inventing a reply or moving research into Dialogue.

## Budgets and cancellation

Each participation, research/action, and dialogue stage has a fresh deadline from its role's configured timeout (60 seconds if no timeout exists in an injected test router). Planner research is additionally limited to six completions and twelve read calls. Dialogue is limited to three completions for formatting/grounding repair. Each tool receipt is limited to 64 KiB; oversize evidence fails explicitly instead of silently truncating claims. Existing explicit router fallback order remains in effect within the stage deadline. No parallel speculative Dialogue generation occurs.

The parent turn cancellation signal reaches every stage. Deadline failures are diagnosed as Planner/Dialogue timeout rather than user cancellation. Model/read planning and pre-send-hook awaits stop promptly even if a trusted extension neglects its signal; this does not claim to undo an already executed external action. Once an adapter has begun an external send, cleanup waits for its actual result. An uncooperative adapter can therefore outlast the dialogue deadline; cancellation-requested is not completion, and unknown delivery is never retried. Core/plugin guards still prevent late sends, and dependent source recalls prevent old evidence from reaching Dialogue. Memory and meme validation/dependency sets survive handoff.

## Delivery and safety

Dialogue sees only its selected delivery tool. Runtime checks plan-selected targets, mention recipients and assets before and after send hooks. Existing scene/simulation isolation, recall tombstones, manual rules, image availability, stale exact-word revalidation, typing delay and platform receipts remain authoritative. A correction during planning or composition is revalidated before delivery; a correction before a planned mutation stops that mutation.

After any settled send, composition stops. The core send guard also rejects a second operation under a different ID within the turn; unknown/failed/cancelled delivery is never automatically repeated. Multiple explicitly composed segments remain sequential within one operation. A plan is never recorded as successful delivery. Existing honest grounding fallback may occur only after rejected composition when no send outcome exists, with existing feedback-target requirements; it is not a fallback for Planner failure.

## Verification

Tests use injected completions or local mocked HTTP endpoints, not paid/real model calls. Coverage includes independently routed Planner search, receipt/URL retention, disabled search, forbidden Dialogue search, vision-before-planning, strict plan validation, noReply, cancellation and recall around awaits, stale corrections, independent deadlines, feedback receipt normalization, plugin send guards, action budgets, and distinct-operation duplicate suppression. Production model quality, external provider availability and actual latency are not established by these mocks.
