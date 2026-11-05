# Explicit source revisions (experimental)

The default-off `sourceEventRevisionsEnabled` setting enrolls metadata for new live messages. It does not enable scene learning, retain original chat text, or run another model. Existing enrolled sources keep revision protection when enrollment is disabled. Normal duplicate message IDs never become edits, even if their body differs.

The trusted adapter API has an explicit `edit` command. It requires a stable edit delivery ID and either a monotonically ordered platform sequence or a producer-supplied exact prior revision CAS. The first edit fixes the ordering mode; switching incomparable modes is rejected. The current SnowLuma transport has no edit-notice mapping, so this release does not claim real QQ editing support. Synthetic adapters exercise the mechanism offline. Historical platform reads cannot create live edits or learning work.

## Retention and interpretation

The event store keeps identity, normalized evidence hashes, server-observed time, nullable occurrence time, and immutable revision references. It never persists source text or signed media locators. Raw Runtime context continues to obey the existing cache switch and TTL; restarting does not replay old chat into an LLM. Default per-event metadata limits are 32 revisions and 128 edit keys. Persistent event heads have no default lifetime message-count quota: the former 5,000-per-scene / 20,000-per-instance cutoff could permanently stop new learning in an otherwise healthy long-running chat. Existing heads are not evicted, rewritten or downgraded; updating the application permits new sources without replaying messages that were previously refused. Explicit `SourceEventStore` embedding limits remain supported and stop new enrollment, not ordinary chat, existing edits, or revocation.

Persistent metadata and recovery evidence grow with enrolled messages, unlike bounded in-memory learning queues and chat context. Operators must provision and monitor disk capacity; this change does not make storage or recovery validation constant-cost. Database/storage failures still fail the operation under the existing transaction and recovery rules, never silently fall back to unversioned learning. No database migration, cleanup or protection toggle is required. Updating does not replay old messages or add a migration-time model call; newly admitted messages resume normal configured learning and may therefore consume its existing model budget. Safety tombstones remain monotonic and are not an evictable chat cache.

Current memory interpretation requires the captured source revision, not merely the current message ID or an equal body hash. A→B→A is still three versions. An edited source can make one AND proof unavailable for current use while an independent OR proof remains valid. Current sources, subject witnesses and observation clocks are projected from the surviving complete proofs. Editing does not itself erase authorized assertion history. Recall remains a separate privacy operation clearing all event revisions and applicable historical derivatives.

Old proofs without a revision stay `legacy_unknown`; after a confirmed edit they cannot silently inherit the new version. Assertion history is not an original-chat archive or a complete as-of query: original source bodies are unavailable, and pruned event metadata cannot be invented. Planner historical/as-of capabilities and persistent commitments are separate subsequent slices.

## Other derived domains and media

Existing habits, meme qualification and experience hints must not rely on an edited legacy source. Independent valid sources can preserve current qualification without deleting the stored history. Experience feedback currently has no event-ref write contract and therefore conservatively rejects edited source/target feedback; this is not full revision-aware relearning of every policy.

Source-qualified image aliases and byte cache keys distinguish captured revisions. Bound-event media does not silently replace a failed locator with a different asset from an unversioned history response. Missing retained source evidence is unavailable, not permission to re-fetch a deleted or changed source as the old image.

## Recovery

The participating reader capability advances when events are enrolled; the authority-chain format stays compatible. Accepted edits append an independent precommit head witness. Restoring an old snapshot cannot overwrite a newer source head; missing exact material stays quarantined. If an external edit intent survived but its new material never committed, the hash alone cannot reconstruct that material and automatic reconciliation may not be possible. See `recovery-authority.md` for custody, model-admission hold and trust limits.

The source-event slice passed 17 actual synthetic Runtime integration cases and the complete 1,010-test suite, contract checks, and runtime/Nuxt type checks on 2026-10-28. Scripted tests establish contracts and isolation, not semantic edit recognition, real platform support, human-like memory or token savings.

When an embedding caller explicitly configures event-head capacity, ordinary replies may continue with unregistered input, but automatic source learning/media intake and contextual proposals from that input are rejected rather than silently downgraded to legacy proofs. An explicit registration-limited diagnostic is recorded.
