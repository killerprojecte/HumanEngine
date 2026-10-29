# Model memory read and personalization controls

Each real scene has two independent controls, both enabled for legacy scenes:

- `memoryReadEnabled`: allow managed model-facing reads of stored long-term information.
- `personalizationEnabled`: allow structured personal records and learned personal/interaction hints. It does not redact current messages or perform semantic privacy classification of arbitrary text.

These are read policies, not deletion, truth changes, or a substitute for source/scope authorization. Administrator inspection, correction, deletion, source recall, recovery gates and deterministic maintenance remain available. `learningEnabled` is independent. Pausing reading does not turn learning off; a fresh source-only text extraction may still save a properly qualified assertion. Deterministic server-side duplicate detection may inspect storage without sending that storage to a model.

## Model-facing behavior

With reading paused, memory tools reject with an explicit read-policy reason. Stored habit/experience hints and stored meme-library recognition are not injected. Background old-memory prefetch/reconciliation and public-knowledge review are skipped. Current chat, manual rules and direct transient vision of a current image remain available. Background stored-media analysis is paused conservatively; this does not imply every kind of learning continues.

With personalization paused but reading enabled, ordinary memory search/source inspection retains only non-person-scoped facts, claims and terms. Experience/summary records and specialized person, plan, relation, working-set, episode, experimental-policy and meme tools are conservatively unavailable. General technical public review may continue. Structured personal hints are omitted. A general record with unmarked personal language is not automatically identified or redacted; the control must not be described as a universal personal-data filter.

Embedding generation of stored scene assertions is also a model read: a paused or excluded batch cannot be sent or activate an incomplete rebuilt index. Existing vectors and assertions are not erased. New assertions can remain lexical-only until an explicit later rebuild. Query embeddings contain the current query rather than retrieved old records; already-started query operations need not be cancelled, but their results do not bypass the model-read authorization check.

## In-flight and queued work

The server owns `memoryReadRevision`; clients cannot set it. Changing either control or the same scene ID's routing identity advances it; display-only renaming does not. A ticket captures the original revision before asynchronous reading. Its policy-only validator may exist even when a retrieved memory group is later omitted; that is not memory proof authority. Omitted records contribute no source dependencies or learning parents. OFF then ON does not renew a previously exposed turn or a queued contextual learning job. The job guard is independent of the original turn's abort signal, so normal turn completion does not cancel valid independent learning. Fresh source-only work has no inherited read ticket.

Managed native/custom physical model attempts recheck the original context/ticket after queue waits, including retries. A request already sent cannot be recalled. Rejected old work is not automatically replayed. Typed candidate writes, source validation and all original privacy/version barriers remain in force.

## Management and recovery

The scene API returns the server-normalized state. Older clients omitting the new flags preserve existing values rather than silently enabling them. Opaque scene-policy revision and ordered safety transitions survive backup recovery. Reader capability 5 is required when the new policy is stored; an older participating binary must fail closed rather than ignore it. This does not authenticate arbitrary maliciously edited backups or control arbitrary trusted plugin code making its own network requests.

The controls do not change manual rules, original-chat retention/TTL, source recall, financial recovery HOLD, or the meaning of `unknown` provider usage. They introduce no model calls of their own.
