# Prompt context window settings

Participation and planning keep separate bounded, append-oriented message windows per
scene, topic, and real/simulation mode. Replyer receives only the messages and evidence
authorized by the current plan; it does not inherit a previous plan or the full planning window. Group chats and private chats do not share windows; simulation contexts
remain isolated. New messages append until the message or byte limit requires rebuilding
with the newest eligible messages. No paid summarization call is used for rebuilding.

This is separate from local history retention (`cacheEnabled`, `cacheHours`) and topic
routing references (`topicHistoryMaxMessages`). Increasing a context window does not
extend source retention, restore recalled messages, or grant permission to read memory.
Revoked and expired data must be excluded immediately, even when that changes a prefix.

| Setting                             | Default            | Allowed values                                                   |
| ----------------------------------- | ------------------ | ---------------------------------------------------------------- |
| `promptContextWindowEnabled`        | `true`             | Boolean                                                          |
| `promptContextWindowMaxMessages`    | `48`               | Integer, 24–200                                                  |
| `promptContextWindowRetainMessages` | `24`               | Integer, 1–100, no greater than `promptContextWindowMaxMessages` |
| `promptContextWindowMaxBytes`       | `131072` (128 KiB) | Integer, 8192–1048576                                            |

These defaults leave room for new messages between rebuilds without unbounded prompt
growth. The retained-message count is an upper bound: the byte budget can require fewer
messages. The byte limit applies to the context window, not the full model request or
the provider's token limit. Role-specific evidence limits still apply independently.
Existing installations without these fields receive the defaults. Disabling the feature
does not erase local history or learned memory; numeric settings remain saved for reuse.

Use Settings → Prompt context window to tune these controls independently from
Temporary messages and diagnostics. Smaller windows reduce history carried forward;
larger windows can preserve more context but may increase request size and cost.
Changing settings takes effect through the runtime's next context selection.

A stable prefix creates an opportunity for provider-side prompt caching, not a guarantee
of cache hits or savings. Provider support, model, minimum prefix length, cache lifetime,
and other request content can affect results. Use reported provider usage to evaluate
actual cache reads and costs; missing counters are unknown, not zero.

## Wire format and freshness

The Router emits each eligible history item as a separate user JSON message, followed
by the current decision, evidence qualifiers, and exact clock. A fixed instruction explains
that these JSON blocks form one context. Model text is not promoted to instructions.
Tool calls and tool results remain intact, and the same transformation is used for native
Chat Completions, Responses, and custom protocol adapters. No provider-specific cache
parameter is guessed. Providers may still merge blocks or apply their own cache policy.

Windows store only message IDs and content hashes. Each turn resolves those IDs from
currently authorized source rows. Missing, edited, expired, or reassigned messages force
a fresh selection; settings, rules, model routes, scene permissions, and tool schemas
are included in the window generation. Source snapshots and live authority guards are
checked again throughout the turn and before model exposure and delivery. Pending
turns cannot continue with an older policy. Memory and learned hints are rebuilt normally,
not copied from a previous request. Short references are allocated in the original trusted
projection order, before changing top-level serialization order.

A single newest message exceeding the configured history byte limit fails safely rather
than truncating its content. A decision target that cannot fit the planning window also
fails safely rather than silently answering without its evidence. The planner preserves
all currently selected participation candidates where the limits permit; the retention
count controls participation-window rebuilds. Both role evidence budgets and serialized
message-block overhead remain enforced separately.

Window membership is transient and bounded to 256 keys with least-recently-used eviction.
There is no restart replay, paid summarization, opaque provider conversation reuse, or
reuse of an old authorization result. Disabling the feature restores the prior last-24
history selection and unsplit request layout.

## Verification and trade-offs

Synthetic runtime checks exercise 24 → 34 → 24 message growth/rollover, recall, and
policy changes while participation is pending. Transport tests reconstruct the exact
logical context from both Chat Completions and Responses message blocks, preserving
chronology, current clocks, source-event references, tool-call pairing, and continuation
identity. History evidence and source-event aliases are registered together in history
order before dynamic topic references, so adding a topic cannot renumber old blocks.
Replyer packing measures the complete serialized user-message block array; planner
packing measures the complete expanded message array. These remain byte budgets,
not claims about a gateway's tokenization or actual cache-hit rate.

The new live configuration guard uses one recovery readiness check and one SQLite
snapshot for settings, models, rules, and the scene. In the existing synthetic durable
send-validation profile (100 and 1,000 ledger entries), the original eight recovery
scans remain eight and the added configuration guard contributes one, rather than
checking each setting separately. It never caches a positive authorization decision.
This is a measured operation-count trade-off, not a production CPU or latency claim.
