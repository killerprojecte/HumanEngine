# Deterministic model-call admission

Automatic live-message turns now pass a non-model policy **before** taking the existing
coalesced topic wake and preparing model context. This reduces calls rather than asking
an already-paid model whether it should speak less often.

| Setting                | Default | Meaning                                                                                                                                                             |
| ---------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `groupTurnCooldownMs`  | 3000    | Minimum start-to-start spacing for ordinary group turns in one conversation. `0` disables this spacing.                                                             |
| `directTurnCooldownMs` | 0       | Independent spacing for private messages, mentions and replies to the bot. `0` means no extra cooldown, not a concurrency/cancellation bypass.                      |
| `sceneTurnConcurrency` | 1       | Maximum active automatic turns per conversation. Ready topic batches wait for a free slot.                                                                          |
| `modelCallsPerMinute`  | 0       | Optional global rolling-60-second model-start budget; `0` disables it. All model roles, retries, embeddings and custom protocols use the existing shared scheduler. |
| `modelConcurrency`     | 4       | Existing maximum concurrent model requests; still applies independently.                                                                                            |

The 3-second ordinary-group default modestly extends the existing 1.2-second quiet
window without slowing the first response. Direct and ordinary-group clocks are separate:
a stream of direct messages must not continually postpone an ordinary-group batch. Both
lanes share the conversation's concurrency limit. These are timing and queue policies,
not keyword commands or model judgments. Simulation/manual turns retain their existing
behavior. Periodic proactive opportunities skip busy/queued/cooling conversations rather than competing
with fresh ingress. An explicit `Runtime.proactive(sceneId)` invocation retains its existing
manual semantics; only the Engine timer passes `scheduled=true`. Manual invocations still
obey model-level concurrency/rate budgets and source/authority validation. Learning retains its existing batch admission. All their model requests
share the optional global start limit and existing concurrency.

Messages still enter history, attention and extension/learning observation before turn
admission. Existing topic buffers retain a coalesced latest-context snapshot; no new job
is allocated per incoming message. Quiet/max-wait bound burst collection; cooldown and
concurrency are additional admission constraints, so they may extend total response time.
At completion, ready batches are considered in pending-batch order. Recall, cancellation,
scene disable, adapter removal and disposal retain existing cleanup/no-restart-replay
rules. Saving timing settings recomputes pending deadlines without resurrecting old input.

The optional global budget counts starts, not successful responses, tokens or money.
Provider failures and retries, including native image-upload adaptation after HTTP 413,
consume starts. Custom protocols are counted at the trusted adapter invocation boundary;
provider requests hidden inside an adapter are the adapter’s responsibility. It uses the existing bounded, abortable
priority scheduler rather than creating another queue. Foreground reservation and
background aging remain unchanged. **Queue time still consumes each model stage's
configured deadline.** An aggressively low global rate can therefore cancel a request
before provider invocation, especially background work; start with the enabled group
cooldown and existing concurrency, then tune this optional limit against measured load.
Cancellation removes queued work and does not spend a start. Disabling the global limit
drains queued work subject to concurrency; it does not retry timed-out operations.

Offline fake-clock fixtures cover burst invocation counts and eventual transport delivery,
separate direct cooldown, scene-slot fairness, recall/disable/dispose cleanup, rolling-window
boundaries, queue bounds and a stage deadline expiring without a provider start. They are
synthetic contract tests, not claims of measured production savings or semantic reply quality.
