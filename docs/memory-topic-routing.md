# Bounded topic routing experiment

`topicAdmissionEnabled` defaults to **false**. OFF preserves the previous routing
baseline; it is not a claim that the baseline understands separate conversations.
The experiment adds no model call per incoming message. It reuses existing turn
participation/planning and changes only bounded ephemeral routing metadata.

## Signals, not new authority

An available reply reference takes precedence over a bounded explicit first-part
`[topic:label]` or `[话题:标签]` text cue. Text cues are untrusted conversation hints,
not permissions, person identity, commitment confirmations or executable tasks.
There is no invented platform thread capability. Participant continuity is used
only when it identifies one eligible conversation; ambiguous input is provisional,
not a claim of semantic understanding. Ordinary unlabelled parallel natural-language
plans are not guaranteed to be separated correctly.

Only an actual acknowledged sent segment with a platform message ID supplies a
reply-to-bot topic anchor. Another segment being unknown does not erase that ACK,
but the operation's partial/unknown state still protects the topic against eviction
or replay. A generated fallback display ID is not an acknowledged platform anchor.

## Capacity and cancellation

There are four topic slots and at most 60 deferred input IDs per scene. Active,
pending, waiting and partial/unknown-send state cannot be evicted to make space.
The 61st unadmitted input is explicitly reported as unrouted overflow; it is **not**
a promise of a later reply. Ingress extension receipt occurs once, even when routing
is deferred. Reconsideration routes a currently eligible retained ID, never calls
`ingest` again and never replays a dispatched operation.

Scene state exposes process-local `topicRouting` counts (`deferred`, `overflowed`,
`provisional`) and limits without body or cue text. Settings changes clear deferred
inputs. Switching this routing flag OFF is not the same as disabling participation:
already admitted work may finish and an existing WAIT may perform its one cheap
recheck, without manufacturing a model wake. Participation OFF, explicit cancel,
adapter replacement, source recall/edit and cache retention still apply their own
existing safety rules. Restart does not replay deferred inputs or old chats.

Model-suggested regrouping is checked as a whole: overlapping/unknown inputs and
unsafe ownership changes are rejected. Consumed wake IDs cannot be observed into a
new topic to obtain another generation or send attempt.

## Recovery and limitations

The flag participates in version-5 configuration authority events. Older flag
versions remain strict and cannot carry this new field; latest recorded OFF is
replayed when restoring an older trusted snapshot. Recovery model HOLD remains
independent and unchanged. Topic state itself is not restored.

Synthetic Runtime tests verify routing, cancellation, capacity and request counts.
They do not demonstrate real-language topic accuracy, better replies, token savings
or real-platform thread support. The finite defaults are engineering bounds, not
experimentally optimized human-memory parameters.
