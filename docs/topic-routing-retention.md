# Bounded process-local topic routing history

`topicHistoryMaxMessages` controls the maximum **unprotected routing-history IDs per scene**. The default is 2,048; the advanced resource setting accepts integers from 500 to 20,000. It is independent of durable memory, source-event authority, and recall tombstones.

## Retention policy

- Routing IDs carry their original observed message timestamp. They expire using the existing `cacheHours` setting.
- The current message-body window (maximum 500 messages) is preferred when the count cap is reached. Other unprotected IDs are evicted in observed ingress order.
- Cache OFF preserves only the existing short pending/in-flight window (60 seconds). Idle or unscheduled cache-OFF cleanup drops unprotected routing metadata immediately.
- Exact pending, active and model-WAIT event IDs remain protected, together with active task source snapshots/dependencies and active send-plan references. Protected IDs may temporarily exceed the ordinary cap and TTL; they are reclaimed when their lifecycle ends.
- Source snapshots are exposed to retention before history/image awaits, not only after dependency collection. This avoids discarding an input while a turn is still preparing its context.
- Unknown/partial delivery protects the topic's identity and no-replay state, **not every old message ID forever**. Receipt state, cancellation, source validation and durable recall authority are unchanged.
- Lowering the setting trims idle real-scene state immediately. No stored chat is reloaded on restart.

The index contains IDs and timestamps only. It does not persist message bodies or create a new disk-backed topic archive.

## Replies to evicted history

An evicted ID no longer proves ownership by an old topic. With topic admission enabled, the existing unavailable-reply/provisional behavior remains. In legacy mode, routing uses the existing no-anchor recency/attention fallback rather than pretending the old ownership is still known. Independently authorized quoted-message retrieval is unaffected.

This is an intentional bounded-history behavior change from indefinitely retaining every topic ID. It does not erase durable memory or authorize resending an uncertain delivery.

## Observability and safety

`topic_history_trimmed` records aggregate counts only after meaningful reclamation, at most once per minute per scene. `topic_history_reply_unavailable` is also rate-limited per scene. Neither log contains message text or the list of evicted IDs.

Focused tests cover actual 650-message bursts in both admission modes, cap shrink, TTL, cache OFF, pending/active/WAIT protection and release, uncertain-send flags, source snapshots suspended before dependency population, and recall/corruption during those awaits. Existing topic isolation, stale-generation, unknown/partial send and no-replay regressions must continue to pass in the merged publication candidate.

This bounds process-local routing history per scene; it is not a claim that all application memory, plugin work or production latency is bounded by this one setting. The separate performance audit records remaining costs and measurement limits.
