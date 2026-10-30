# Bounded Planner reference correction

A real synthetic API check found a valid-looking `reply.plan` that used a memory record ID in both `memorySelections.evidenceId` and `memoryIds`. These identify different objects:

- `evidenceId`: the outer **tool receipt** ID, for example the receipt returned by `memory.person`.
- `memoryIds`: IDs of selected records **inside that same receipt's value**.

The original validator correctly rejected the plan. It must not guess which receipt the model meant, merge source scopes, or silently upgrade an old reference. A later offline native-wire replay reproduced the precise `planner_unknown_memory_evidence` failure. This was not the six-round Planner limit being exhausted.

## Correction contract

For explicitly classified plan-structure/reference errors, the Planner can request one correction within the existing six Planner rounds, twelve read-tool calls, byte, time and model-admission budgets. It can consume one additional model completion within those original six rounds; terminal `reply.plan` calls are not part of the twelve read-tool counter. The correction response supplies a fixed error code and bounded ID-only options from receipts that were actually delivered in this turn. Limited or omitted evidence remains limited; the hint does not grant access to previously omitted records.

Only `reply.plan` is offered during this correction. No new read or action tool is executed, and no new exposure is registered. The corrected plan passes the unchanged full validator and eligibility checks, including checks after asynchronous model completion. Source recall, permission changes and version changes cannot be repaired into authorization. The correction cannot add or change actions. A second invalid result fails explicitly. Provider errors and transport uncertainty do not trigger this correction.

This is a usability improvement, not proof that the generated answer is true or that every model will correct its output. A valid plan still passes through ordinary Replyer and send safety checks.

## Diagnostic scope

`planner_unknown_memory_evidence` and `planner_incomplete_relation_evidence` have fixed safe codes and localized explanations. They do not include model arguments, source text, credentials or provider error bodies. Administrative diagnostics and the test report distinguish model transport success, persistent learning, valid planning, and successful reply delivery.

## Evidence and limitations

The first authorized real test used the published pre-fix source. Both providers returned a successful minimal generation. Actual source learning persisted two records and read them after restart, but the Planner reference error prevented Replyer execution. That result is retained as a failure, not retroactively converted into a pass. Subsequent offline regression results and any separately authorized post-fix live results must be reported separately. Provider-reported tokens are not a verified dollar cost; no dollar hard cap was claimed.

### Post-fix authorized API check (2026-10-30)

On `deepseek-v4.1-flash`, the DS endpoint completed a real synthetic source-learning → two committed records → close/reopen → memory tool reads → valid Planner plan → Replyer → local synthetic adapter acknowledgment. The reply correctly retained the jasmine-tea / no-sugar preference. The model produced valid references on its first plan in this run; it did **not** exercise the correction round. The correction branch is covered by offline native-wire replay and authorization regressions, not claimed as a real-model repair success.

Gitee completed the minimal generation check, but its subsequent tool-bearing learning request returned HTTP 400 without usable usage data. The host stopped that batch; no memory was written for that provider. The error body was not retained, so the precise provider compatibility cause remains unknown. General tool-support claims do not establish support for this exact schema. This is a separate unresolved compatibility result, not a reason to weaken plan validation or mark both providers end-to-end successful.

The post-fix batch made ten physical requests: nine had reported token usage and one remained unknown. It used only synthetic inputs and local adapter acknowledgments, never a real chat platform. Neither broad semantic quality nor verified monetary cost is established by these small checks.
