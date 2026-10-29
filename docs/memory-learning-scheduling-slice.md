# Asynchronous learning and bounded admission

This slice changes execution boundaries, not model quality. All validation uses synthetic inputs and deterministic/local mock model responses. Real-model quality, token savings, and human-like memory remain unestablished.

## Modules and authority

- `memory/learning/contracts.ts` owns proposal schemas.
- `memory/learning/coordinator.ts` owns the existing bounded extraction/reconciliation/persistence batch workflow, separated from Engine. Its proposal port is source-only; it cannot choose contextual provenance.
- `memory/learning/admission.ts` owns clock-driven quiet/max/minimum windows and metadata, not model calls or write authority.
- `memory/learning/contextual-proposals.ts` captures trusted scene/source/parent version authority. `contextual-queue.ts` dispatches bounded ephemeral candidates independently of normal reply completion.
- `runtime/turn-wait.ts` owns topic generations and cheap wake admission. It never sends or infers delivery outcomes.
- `memory/storage/projection-outbox.ts` and `memory/projections/*` provide optional deterministic derived projections, not authoritative memory.

## Runtime behavior

Live non-bot messages already entered learning independently of reply selection. That path is preserved, including silent participation. A Planner `noReply` plan may now enqueue `learning.propose`, but not send media or execute other actions. A queued receipt means accepted for later processing, never saved or remembered. Replyer does not wait for extraction/indexing/public-review work.

Contextual jobs retain the source and trusted exposed-parent snapshot. Asynchronous search results are checked against a fresh authorized record before handoff; changed bodies or metadata are omitted rather than stamped with a newer revision. Ranking scores are not authority. The actual accepted record and revision are captured together before returning to the model. Learning OFF, source recall, changed parent version/epoch, or disposal prevents stale work from committing. Normal `turn_finished` does not cancel a separately accepted job. Already committed valid facts are not rolled back merely because later indexing is cancelled. Failures/unknown partial effects are not automatically replayed. Both queues are ephemeral: restart does not re-learn retained chats.

Repeated identical contextual candidate/source/parent jobs are deduplicated within a bounded in-memory window. The authoritative memory service still deduplicates records and support groups. Contextual proofs and independently established source-only proofs are not interchangeable: background extraction is not skipped wholesale merely because one foreground candidate mentioned the source. Other facts in that message must not disappear.

## Configuration and priority

`learningBatchMs` is now the quiet window (existing default 30 seconds); `learningMaxWaitMs` defaults to 120 seconds; `learningMinIntervalMs` defaults to 60 seconds. Continuous arrivals coalesce until the maximum, subject to the minimum interval and foreground availability. These are not periodic model clocks. Updating settings preserves pending sources and recomputes future admission. Batches consume exact admission source keys rather than payload array positions; pre-commit recall may reorder survivors. Recovery reconciles only authorized pending payloads, never active, failed, completed, or unknown-effect jobs. Disabling a scene clears its pending work.

Model priority is foreground reply/decision/required vision, then realtime learning, then maintenance. Queued tasks preserve their submission async context, including nested model calls, rather than inheriting the previous running task’s priority scope. With concurrency at least two, one slot is reserved for foreground requests. A running request is not falsely marked cancelled to free capacity. Concurrency one cannot preempt an already-running background request. Waiting maintenance ages to realtime priority after five minutes, never above foreground. This improves bounded opportunity among non-foreground tasks; it is not a latency guarantee under an infinite foreground workload.

A Planner WAIT may specify `waitForMs` from 0 to 60000. Expiry performs one cheap recheck. Without fresh input it does not call a model. New inputs still respect their own quiet/max window; an old WAIT cannot force premature model calls. Consumed inputs and sent/unknown segments are never replayed by the admission helper. Existing final send validation and transport receipt handling remain authoritative.

## Experimental projections

`memoryProjectionEnabled` defaults to false. When enabled, newly committed revisions enqueue metadata-only lexical/person projection tasks in the same transaction. The consumer rechecks revision/epoch and support before writing shadow FTS5 and a content-free person freshness marker. Authoritative reads are unchanged; no retrieval-speed or quality gain is claimed. Future-dated facts may be indexed but remain excluded by current applicability rules.

Disabling the experiment prevents new jobs and consumption. Changes, recall, and deletion still remove obsolete derived text. Restart may recover already-committed projection tasks only; it never starts old chat extraction, embeddings, model calls, or sends. Work is drained in bounded batches at commits/start/settings changes; very large residual backlogs may require another drain. Projection failure does not roll back committed authoritative facts. Current-state idempotency metadata and active jobs are retained; old terminal metadata is pruned conservatively.

## Remaining work

Parent proofs still reference IDs rather than immutable selected revisions; complete as-of evidence replay is not implemented. Full graph retrieval, budget admission, policy adaptation, and real-model semantic evaluation remain later slices. Old-binary rollback and stale-backup restoration must not be advertised as deletion-safe without the separate deletion-ledger migration work.
