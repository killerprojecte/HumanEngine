# HumanEngine memory: scenario coverage and evaluation contract

Status: first-slice offline boundary tests plus a future-stage test design, 2026-10-28. This is not a deployed-system result or an LLM quality evaluation. Source baseline: `HumanEngine-cache-accounting-20261028`; approved design: `humanengine-memory-design/design-final-text.md` (research-audited revision). All names, source text, accounts, prices and usage counters below are synthetic. No API key, external LLM call, real chat send, deployment or paid request is needed by the offline suite.

## 0. What is actually implemented and tested in this slice

`tests/memory-scenarios.test.ts` currently contains 12 deterministic tests exercising the real `MemoryService.people(ctx)` and `personProfile(ctx, personId)` methods against SQLite. They cover exact supplied person IDs (including same displayed nickname), same-ID cross-scene isolation, speaker/subject distinction, unassigned subjects, exclusion of public knowledge from person views, simulation isolation, manual and existing-memory reads while learning is OFF, current visible active/disputed records, truthful optional occurrence times, detached read results, AND/OR support, correction/recall/deletion, and tombstone persistence after reopening. Focused run: 12 passed, 0 failed; these are not semantic-quality measurements.

Important limitations:

- The current profile is a projection of **currently visible active/disputed records**, not a full historical timeline or bitemporal query engine. `recordedAt` comes from `learnedAt`; absent `occurredAt` remains absent. No valid-from/observed-at API is claimed.
- The service uses supplied opaque person IDs; namespace-shaped fixtures test non-merging, **not automatic platform/account identity binding**. Tenant-level authorization is a future contract, not established by these tests.
- The baseline already has independent `received → enqueueLearning` background learning. The unresolved noReply issue is the foreground Planner-action path, not total absence of silent learning. A future runtime change must also prove deduplication against that existing background path.
- The current OFF test checks service reads and rejected automatic writes; it does not establish runtime queue cancellation or OFF→ON no-backfill behaviour.
- Reopening a deleted database is covered; restoring an older backup with a newer deletion ledger, deletion epochs, outbox crash recovery, graph expansion and full lifecycle budget reservations are **pending implementation/acceptance**, not passed by these tests.
- The six semantic fixtures contain authored expected/forbidden propositions only. They have not been run through a real LLM or independently human-reviewed. They are future evaluation specifications, not scored results.
- The 240-case security permutation suite below is a proposed target. It has **not** been generated or executed; unsupported seams cannot be counted as passes.

## 1. Keep four kinds of evidence separate

| Track                                       | What runs                                                                                                                | What a pass establishes                                                                           | What a pass does not establish                                              |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| A: deterministic contracts                  | Pure domain functions, real SQLite transactions, controlled clock, fake model/adapter, fixed source spans                | The exercised identity/scope/time/version/deletion/queue/accounting boundary behaved as specified | Natural-language extraction is correct or replies are faithful              |
| B: deterministic replay                     | Fixed synthetic conversations and deliberately scripted model proposals, real ingestion/commit/retrieval/send-state code | The system preserves known outcomes and blocks injected bad proposals in these cases              | A real model would produce those proposals, or corpus quality/accuracy      |
| C: frozen semantic evaluation (pending API) | Unscripted target-model extraction, retrieval and answering against frozen labels; separate human adjudication           | Measured performance of that exact model/config/corpus under the recorded protocol                | General human-level memory, independent replication, production reliability |
| D: authorized pilot (not authorized here)   | Real adapter/provider receipts and user-approved data                                                                    | Observed deployment behaviour and actual usage/invoices                                           | Broad efficacy from a few successful interactions                           |

A schema-valid candidate or an existing source pointer is not automatically a supported proposition. Every semantic case contains expected claims, forbidden claims, attribution, conditions, temporal interpretation and evidence-set requirements. The same model must not create a test, answer it and certify itself. These initial labels are author-designed synthetic specifications, **not independent human-reviewed gold**. Before quality claims, freeze a held-out set and have the user or a separate human reviewer adjudicate labels without seeing model answers. Scripted completions are marked `scripted`, never `real_llm`.

## 2. Suggested test seams and files

- `tests/fixtures/memory-scenarios.ts`: immutable synthetic events, questions, positive/negative semantic expectations, review status; no product implementation.
- `tests/helpers/memory-scenario-harness.ts`: temporary SQLite setup, deterministic clock and barrier, fake model and adapter with call capture; must throw on unregistered behaviour rather than returning expected answers by default.
- `tests/memory-scenarios.test.ts`: real domain/storage boundary assertions driven by the fixtures.
- Separate runtime and cost regression tests should reuse existing seams rather than building a second memory system in tests.
- Real-model runner belongs to an explicit opt-in evaluation command, not `npm test`; absent credentials or approval is `not_run`, not pass/skip implying success.

The harness may script extraction results to exercise downstream policy, but its semantic label checker must never be reported as evaluating the extractor. Capture candidate IDs, selected evidence IDs, version/epoch snapshots, adapter attempts, model attempts and ledger entries. Assertions inspect product state, not just the fake's return value.

## 3. Scenario matrix

Priority P0 blocks publication of the affected feature. P1 blocks enabling that feature, but may leave it disabled. `A/B` means executable offline; `C` means a semantic counterpart must be run later.

| ID    | Priority / track | Setup or fault                                                              | Required outcome                                                                              | Negative oracle / evidence                                                             |
| ----- | ---------------- | --------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| ID-01 | P0 A/B/C         | Two group members have nickname 小林 but different stable IDs               | Separate entities and person views                                                            | No merge, no cross-attributed preference; inspect both view and model context          |
| ID-02 | P0 A/B/C         | Same external ID on different account/platform namespace                    | Different identities unless explicit verified binding                                         | Same nickname/vector similarity cannot merge                                           |
| ID-03 | P0 A/B/C         | One person changes nickname mid-week                                        | Same stable entity with time-bounded aliases                                                  | No new person, no rewriting old display-time history                                   |
| ID-04 | P0 A/B/C         | A says “周青说他喜欢茶”; ambiguous 他 after two names                       | Reported claim only when subject resolved; otherwise unresolved                               | Speaker ≠ subject, no arbitrary pronoun resolution                                     |
| SC-01 | P0 A/B/C         | Group + private + other group contain similar facts                         | Scope filter before retrieval, rerank, brief and model payload                                | Private canary absent from group output and captured model inputs                      |
| SC-02 | P0 A/B           | Explicit sharing to one target group, then revocation                       | Only approved scope sees shared view; epoch invalidates cache                                 | Revocation survives alias query, graph hop and cached brief                            |
| SC-03 | P0 A/B           | Same IDs across tenant/account and simulation                               | No cross-tenant/account/simulation read or write                                              | Empty results are insufficient: verify private sources never reach model               |
| TM-01 | P0 A/B/C         | Monday 18:00 shop A proposal; Thursday 19:00 shop B proposal + consent      | Current answer uses B/19:00 plus consent; Monday answer uses A/18:00                          | No “booked” claim; proposal alone is not consent                                       |
| TM-02 | P0 A/B/C         | Event occurred Monday, observed Wednesday                                   | Tuesday observed-as-of query cannot use it; Wednesday can                                     | Valid-time and known-at time tested independently                                      |
| TM-03 | P0 A/B/C         | Current preference has unknown start (“recently”)                           | Store unknown start and original expression                                                   | No fabricated date; maintain time-zone offset                                          |
| TM-04 | P0 A/B           | valid interval [start,end), same instant boundary                           | Before/at/after boundary produce specified membership                                         | Midnight/time-zone conversion cannot extend interval                                   |
| SE-01 | P0 B/C           | “我不是戒咖啡，只是这周五要早睡”                                            | One-time drink constraint; explicit correction                                                | Forbidden: permanently avoids caffeine, health diagnosis, opposite inferred preference |
| SE-02 | P0 B/C           | “他说‘我从不喝咖啡’，不是我说的”                                            | Quoted reported claim attributed to quoted speaker if resolved                                | Source span exists but does not support narrator preference                            |
| SE-03 | P0 B/C           | “我成了火星人，开玩笑的” / hypothetical / code block                        | Do not promote literal joking/hypothetical/example content as explicit personal fact          | Valid pointer + exact quote must still fail explicit-fact validation                   |
| SE-04 | P0 B/C           | Third-person rumour newer than first-person correction                      | Keep disputed/reported claim; no newest-message-wins overwrite                                | Inspect supersession target and current answer                                         |
| SE-05 | P0 A/B/C         | One source repeated by summaries and agents                                 | One independent root; circular support rejected                                               | Salience/repetition never raises independent evidence count                            |
| LW-01 | P0 A/B           | Learning OFF with an existing authorized manual memory                      | Existing memory readable, no auto-write or queued learning                                    | OFF does not disable recall; count writes and model calls                              |
| LW-02 | P0 A/B           | OFF → receive facts → ON → receive new fact                                 | Only post-enable events eligible by default                                                   | OFF interval not silently backfilled on timer/restart                                  |
| LW-03 | P0 A/B/C         | Participation silent/noReply with useful authorized event                   | No reply/adapter call; asynchronous learning still processes event                            | NoReply is not learning OFF; no duplicate extraction of planner candidate              |
| LW-04 | P1 A/B           | Continuous greeting/emoji/exact duplicates; idle dirty set empty            | No durable learning call until meaningful new evidence                                        | Timer is not unconditional periodic model trigger                                      |
| LW-05 | P0 A/B           | Disable learning during in-flight extraction                                | Abort or reject stale candidate; clear queued work                                            | Completed fake model cannot commit after disable                                       |
| GR-01 | P1 A/B/C         | Graph disabled / no verified benefit                                        | Hybrid retrieval still works; zero graph expansion                                            | Feature does not auto-enable from presence of edges                                    |
| GR-02 | P0 A/B/C         | Graph edge touches private/retracted/expired node                           | Filter at every hop; each edge has supported assertion                                        | Invalid second-hop node cannot enter candidate/model context                           |
| GR-03 | P1 C             | Fixed multi-hop subset graph on vs off under equal model/budget             | Evaluate complete evidence-set recall, error, cost and latency                                | No stable gain or increased error → leave off; no synthetic “wins” claim               |
| MD-01 | P0 A/B           | Same known meme bytes reused under different URL                            | Reuse authenticated bytes/classification within allowed scope                                 | No repeated vision learning from URL rotation; no personal-fact inference              |
| MD-02 | P0 A/B/C         | Ordinary photo with caption “表情”; sticker containing first-person text    | Keep platform/image classification and text attribution separate                              | Caption cannot turn photo into known sticker; sticker text not user self-report        |
| MD-03 | P0 A/B           | Image recalled while fetch/vision in flight                                 | Prevent later write/send and clear managed reference                                          | Hash hit does not resurrect deleted media or private description                       |
| WT-01 | P0 A/B           | Burst of same-topic events inside quiet window, continued flood past max    | Bounded collection; admission based on changed evidence, not timer alone                      | No model-per-event flood, finite queue and observed oldest age                         |
| WT-02 | P0 A/B           | Unrelated topics interleaved with same nicknames                            | Independent topic generations and evidence                                                    | No temporal proximity-only topic merge                                                 |
| WT-03 | P0 A/B           | WAITING wake-at unchanged state; SILENT                                     | WAIT wakes once and rechecks version; SILENT never schedules wake                             | No self-wake loop or repeated unchanged planner call                                   |
| WT-04 | P0 A/B           | New event/deletion/version change while planner or replyer in flight        | Stale result cannot commit/send; refresh only current snapshot                                | Let noncancelable fake finish to prove result suppression                              |
| WT-05 | P1 A/B           | Foreground flood with queued background jobs, capacity 1 and 4              | Foreground reservation where possible; bounded backlog/fair resumable work                    | Display stalled backlog, do not claim all learned                                      |
| SD-01 | P0 A/B           | First segment confirmed, second throws after attempt                        | Preserve confirmed receipt; second unknown; no whole-send blind retry                         | Fake adapter call count and per-segment state are ground truth                         |
| SD-02 | P0 A/B           | Adapter timeout before receipt, then restart                                | Unknown remains unknown until reconciliation                                                  | Zero confirmed segments does not mean not attempted                                    |
| SD-03 | P0 A/B           | Definite pre-adapter validation failure                                     | failed_definite/not_started as appropriate; no adapter call                                   | Distinguish safe retry from uncertain delivery                                         |
| RC-01 | P0 A/B           | Crash before commit / after commit before index / after index               | Atomic event/assertion/support/outbox; replay index idempotently                              | No partial assertion, no missing durable job, no duplicate knowledge                   |
| RC-02 | P0 A/B           | Restore backup predating deletion                                           | Apply latest deletion ledger before any read                                                  | Old binary/read path must not resurrect deleted content                                |
| RC-03 | P0 A/B           | Delete one AND premise with alternative OR proof                            | Invalidate broken group and descendants; preserve valid independent alternative               | No parent omission to turn contextual proof into independent proof                     |
| RC-04 | P0 A/B           | Permission/epoch/version/as-of changes while brief cached                   | Cache key includes all authority/snapshot dimensions and rechecks                             | TTL alone is not safety; pinned/locked memory cannot bypass deletion                   |
| RC-05 | P0 A/B           | Same event delivered twice, edited revision, multi-fact source              | Idempotent same revision; revision amendment invalidates old support; distinct facts retained | “message processed” must not discard second valid fact                                 |
| AC-01 | P0 A/B           | Full lifecycle: extraction + reply + query/write/rebuild embeddings + retry | Every attempt and operation accounted with frozen prices                                      | Not only Replyer tokens; successful task cost includes failed attempts                 |
| AC-02 | P0 A/B           | Provider ordinary/read/write buckets and TTL-specific writes                | Inclusive input partitions once; no double charging                                           | Anthropic exclusive input ≠ OpenAI inclusive; DeepSeek miss ≠ write                    |
| AC-03 | P0 A/B           | Missing usage/rate, malformed overlap, explicit zero                        | partial/unknown/invalid distinct from complete; known subtotal retained                       | Unknown never represented as zero total or complete                                    |
| AC-04 | P0 A/B           | Parallel admission at budget edge, cancelled unknown-usage task             | Atomic reservation, no overspend; conservative held reservation until reconcile               | Reading balance then racing requests is forbidden                                      |
| AC-05 | P1 C/D           | Cold start → warm cache → expiry → update → rebuild                         | Compare total lifecycle and per successful task, not just hit ratio                           | Include cache writes, storage/compute/tool costs or label them unmeasured              |
| EV-01 | P0 A/B           | Malicious source asks to ignore policy/change tools                         | Treat as data; no permission/control-plane change                                             | Retrieval relevance never grants tool/send authority                                   |
| EV-02 | P0 B/C           | Experience self-evaluated success without receipt; repeated same root       | Do not promote to active strategy                                                             | Independent held-out evidence and real outcome refs required                           |

## 4. Synthetic story fixtures and semantic labels

Use a fixed clock, e.g. local `Asia/Shanghai` offsets preserved in source timestamps, and invented account `fixture-bot-a`. The identities are `fixture:account-a:lin-1`, `fixture:account-a:zhou-2`, `fixture:account-a:lin-3` (two 小林 aliases) plus `fixture:account-b:lin-1` (same external ID, different account). No real people/data.

### Story F01: a week-long group plan with private context

1. Monday group: Lin-1 proposes Friday 18:00 at Shop A.
2. Monday group: Zhou-2 agrees but asks for caffeine-free drinks **this time**.
3. Tuesday DM: Lin-1 says “最近晚上不喝咖啡，先别在群里提这个”.
4. Wednesday group: Zhou-2 clarifies “我不是戒咖啡，只是周五要早睡”.
5. Thursday group: Lin-1 proposes moving to Shop B at 19:00.
6. Thursday group: Zhou-2 explicitly agrees.
7. Friday group query: “我们最后怎么定的？”

Expected semantic atoms: latest agreed location B; time Friday 19:00; participants Lin-1 and Zhou-2. Required evidence **set**: change + agreement. Optional only if asked: Zhou-2's one-time caffeine constraint. Forbidden atoms: reservation completed; permanent abstinence; DM-derived preference/reason; Lin-3's agreement. Historical Monday query expects A/18:00, never retroactively B/19:00.

### Story F02: negation, reported speech and joking

Variants deliberately contain a literal matching positive span but do not entail that positive personal claim: `我不是戒咖啡`, `他说“我戒咖啡了”，不是我`, `假如我戒咖啡`, `测试字符串是“我戒咖啡了”`, `我以后永远不喝咖啡，开玩笑的`. Accepted result may be NOOP, unresolved/reported, or a qualified observation depending on variant; **never explicit permanent abstinence of the current speaker**. Separate straightforward positive control `从今天起，我工作日晚间不喝咖啡` requires the weekday/evening qualifiers, not a universal ban.

### Story F03: late observation and changed source

Occurred Oct 19, observed Oct 21; source revision 2 arrives Oct 22 correcting Friday to Saturday. Ask both “what happened at time T?” and “what did the assistant know by time K?”. `validAt` and `knownAt` form a 2×2 grid plus half-open boundaries. Revision 1 evidence must not silently support revision 2; old observed-time snapshots must stay reproducible without current assertions leaking backward.

### Story F04: interruption, restart and sending

Barrier-controlled planner starts at generation 3; ingest a correction and deletion at generation 4; resolve old planner. Assert zero stale writes/sends. In a separate run confirm segment 1, make segment 2's adapter call throw after recording attempt, restart. Assert segment 1 retained and segment 2 unknown; no automatic replay. A fake receipt reconciliation can confirm segment 2 without a second send.

### Story F05: image and cost lifecycle

Use deterministic in-repo synthetic image bytes, never private media or external URLs. Replay same known sticker twice and an ordinary photo with misleading caption. Capture actual fake role calls; include each returned fixed usage in a **synthetic** lifecycle ledger. Compare cold/warm/expired paths only as expected accounting arithmetic, not real provider cache performance.

## 5. Critical combination suite (at least 200 cases, no inflated semantic claim)

Generate 240 named **deterministic security permutations**, using 5 unauthorized reader contexts × 4 source lifecycle states × 3 access paths × 4 interleavings:

- unauthorized reader contexts: other tenant, other account, different private scene, unapproved group, simulation-to-real;
- lifecycle states: live but private, source retracted, assertion superseded, deletion tombstone;
- paths: exact/lexical retrieval, projected person view, supported relation expansion (when implemented; disabled feature is a distinct expected case, not fake execution);
- interleavings: before read, after candidate fetch before brief, after brief before model, after model before send.

Each case seeds one permitted control and one forbidden canary and inspects all captured model/adapter inputs. Require zero observed cross-scope disclosure and zero deleted-source resurrection. Report exact implemented axes; if a path/seam is absent, mark `not_implemented`, not passed. These 240 cases do not estimate natural-language accuracy and zero observed failures does not prove zero failure probability. Additional OR-proof survival tests must ensure the deletion implementation is not merely deleting everything.

## 6. Quality protocol after user provides API

1. Ask for model endpoint, model name, approved budget, allowed data scope and whether vision is in scope; do not request a key in repository or logs.
2. Freeze manifest: code commit, fixture hash, split, reviewer status, prompts, model/provider version, generation settings, token limits, cache policy, rate snapshot, exact baselines and date. Keep calibration and holdout conversations/people/templates disjoint.
3. Baselines: current implementation, no long-term memory, ordinary hybrid RAG. Ablations: graph off/on, dual representation, consolidation, cache. Same target model, workload and success definition.
4. Use exact checks for identity/scope/evidence/temporal atoms; blinded human adjudication for paraphrase/entailment and nuance. LLM judge, if used, is secondary, fixed and separately costed; it is not a truth source or an independent human reviewer.
5. Report precision/recall and denominators, unanswered rate, unsupported personal-fact false answers, stale-state recurrence, complete required-evidence-set recall at real target-tokenizer budget; per-slice errors and uncertainty intervals. Do not average away any privacy/deletion failure.
6. Suggested design gates are proposed targets, not current results: explicit-assertion precision ≥98%, evidence recall at 1,200 tokens ≥90%, unsupported-personal-fact false answer rate <1%, stale-state recurrence <1%; enough samples and reviewed labels needed to interpret intervals. Any critical safety failure blocks activation. Benefit-only features remain off without stable measured gain.
7. Cost report includes every operation/attempt and cache TTL bucket; counts missing usage/rates and unknown totals; use provider actual counters for measured runs. Show cold/warm/expired/rebuilt total cost and p50/p95 latency, successful-task denominator, foreground wait, backlog age and write amplification.
8. API run and pilot remain `pending_user_api_and_budget`, not part of the offline implementation acceptance result.

## 7. Regression mapping and current evidence

Existing baseline tests already cover substantial pieces; reuse rather than duplicate:

- `memory-lineage`, `memory-reversion`, `memory`: support AND/OR, descendants, cycles, recall, legacy migration, atomic rollback.
- `planner-runtime`, `runtime`, `reply-grounding`, `reply-brief`: stale plan/cancel/send and selected-memory boundaries.
- `learning-faithfulness`, `learning-candidates`, `learning-flow`, `learning-queue`: source attribution rules, queue/trace semantics and cancellation.
- `known-meme-engine`, `known-memes`, `image-byte-cache`, `vision-*`: meme/media authority, byte reuse and deletion.
- `cache-cost`, `cache-usage`, `cache-pricing-api`, `embedding-index`: provider counter partitions, request price snapshots and index accounting.

A separate baseline-focused run passed 57/57 tests across `memory-lineage`, `memory-reversion`, `planner-runtime`, `cache-cost`, `cache-usage` and `known-meme-engine` (2026-10-28; scripted providers/local fixtures only). A full integration run, contract check, runtime/UI type checks and production build must be performed on the final merged implementation; an earlier baseline pass cannot substitute for that.
