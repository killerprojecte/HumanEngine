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

`learningBatchMs` is now the quiet window (existing default 30 seconds); `learningMaxWaitMs` defaults to 120 seconds; `learningMinIntervalMs` defaults to 60 seconds. Continuous arrivals coalesce until the maximum, subject to the minimum interval. Foreground work delays ordinary batches, but does not indefinitely block an overdue batch: at most one overdue batch may start across scenes while foreground work remains active, selected oldest-first. The existing maximum wait determines eligibility, not a guaranteed completion time. These are not periodic model clocks. Updating settings preserves pending sources and recomputes future admission. Batches consume exact admission source keys rather than payload array positions; pre-commit recall may reorder survivors. Recovery reconciles only authorized pending payloads, never active, failed, completed, or unknown-effect jobs. Disabling a scene clears its pending work and any cancelled batch survivors. A trusted edit replaces the old in-flight source for future admission; the cancelled runner cannot requeue that stale revision. Unaffected sources are retried only if no commit began.

Model priority is foreground reply/decision/required vision, then realtime learning, then maintenance. Queued tasks preserve their submission async context, including nested model calls, rather than inheriting the previous running task’s priority scope. With concurrency at least two, one slot is reserved for foreground requests. A running request is not falsely marked cancelled to free capacity. Concurrency one cannot preempt an already-running background request. Waiting maintenance ages to realtime priority after five minutes, never above foreground. Learning requests carry their batch maximum-wait deadline through asynchronous model scheduling, including prerequisite requests. Once that deadline passes, at most four foreground request starts may precede one oldest eligible learning request at an available non-foreground slot. This does not increase concurrency or the configured calls-per-minute limit. With one slot, fairness means bounded next-slot rotation, never interruption of an in-flight foreground request. A long-lived stream occupying every slot can still prevent progress: no absolute wall-clock completion deadline is promised. Unrelated maintenance requests do not acquire this learning exception. Reaching maximum wait never overrides the hard batch minimum interval, authorization, budget checks, cancellation, or partial-write no-replay protection. Busy installations may now spend their configured learning budget on work that previously remained queued indefinitely; extraction is still batched, not one paid call per incoming message.

A Planner WAIT may specify `waitForMs` from 0 to 60000. Expiry performs one cheap recheck. Without fresh input it does not call a model. New inputs still respect their own quiet/max window; an old WAIT cannot force premature model calls. Consumed inputs and sent/unknown segments are never replayed by the admission helper. Existing final send validation and transport receipt handling remain authoritative.

## Experimental projections

`memoryProjectionEnabled` defaults to false. When enabled, newly committed revisions enqueue metadata-only lexical/person projection tasks in the same transaction. The consumer rechecks revision/epoch and support before writing shadow FTS5 and a content-free person freshness marker. Authoritative reads are unchanged; no retrieval-speed or quality gain is claimed. Future-dated facts may be indexed but remain excluded by current applicability rules.

Disabling the experiment prevents new jobs and consumption. Changes, recall, and deletion still remove obsolete derived text. Restart may recover already-committed projection tasks only; it never starts old chat extraction, embeddings, model calls, or sends. Work is drained in bounded batches at commits/start/settings changes; very large residual backlogs may require another drain. Projection failure does not roll back committed authoritative facts. Current-state idempotency metadata and active jobs are retained; old terminal metadata is pruned conservatively.

## Remaining work

Parent proofs still reference IDs rather than immutable selected revisions; complete as-of evidence replay is not implemented. Full graph retrieval, budget admission, policy adaptation, and real-model semantic evaluation remain later slices. Old-binary rollback and stale-backup restoration must not be advertised as deletion-safe without the separate deletion-ledger migration work.

## Diagnosing missing learning

New scenes start with learning disabled. Enabling learning affects new live messages only; old chats are not replayed. Background extraction requires a configured `learning` model route with both `text` and `tools` capabilities. A missing route or incompatible capabilities can fail before any provider request is sent. Check learning admission and batch diagnostics as well as model-call logs; a queued input is not a saved memory, and an empty extraction is permitted for casual conversation.

Planner contextual learning is optional and uses `learning.propose` inside `reply.plan.actions`, not a direct tool invocation. It can propose explicit durable current-source facts without requiring user feedback, but must not save guesses, transient chatter, or reconstructed retrieved memories. Disabled scene learning is omitted from the Planner action list and remains rejected at execution. Silent/wait participation still feeds the existing background queue without forcing an extra planning call. Rejected tool receipts are logged as failed; queued/duplicate receipts retain that state rather than claiming persistence.
