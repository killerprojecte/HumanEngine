# Contextual learning dispatch and trusted proposal boundary

Port of `src/memory/learning/contextual-queue.ts` and
`src/memory/learning/contextual-proposals.ts` at source commit
`1332561534d6c07b5be824034c59bd84507c732a`.

`Queue` is ephemeral and single-active. The existing source defaults are exactly
128 admitted jobs (active plus pending), 512 remembered terminal identities, and
100 ms foreground polling. These are source product policy, not new limits.
Explicit zero options reject. Identity is scene, candidate key and deduplicated
UTF-16 sorted source IDs. Remembered identity eviction is insertion FIFO. Failed,
discarded and cancelled jobs are remembered too and never automatically retried.
Cancellation of pending jobs emits terminal events in source reverse order;
cancellation of active jobs cancels its independent context but retains the
physical active owner until Run returns. Saved receipts keep committed=true even
when cancellation wins. Unknown errors/panics never enter events.

`WhenIdle` waits for admitted jobs to physically finish. `CloseAndWait` seals
admission, cancels pending/active jobs and joins running jobs, the worker, and
in-flight admission validation and event observers. A timeout reports incomplete drain and does not
permit closing borrowed database/model owners. Callbacks may reenter ordinary
queue methods but must not synchronously wait for their own lifecycle to finish.
The notification channel's capacity of one only coalesces wakeups; it is not a
job admission cap. Foreground uncertainty fails closed.

`Proposals.Submit` deep-copies candidate/evidence and copies typed exposure refs.
It preserves actual source rejection order, dynamic feature policy, original
source snapshot identity, current tombstones, visible-memory revision state,
trusted relation/commitment/knowledge guard requirements and trusted read-policy
identity. Settings revision is captured for diagnostics, not added as a blanket
invalidation rule absent from the source. Schema reconstruction preserves Zod
property order, including contextual existing-entity knowledge support; the
source-only learning parser keeps its existing stricter rule.

`learningowner.Owner.NewContextual(compare)` binds the actual native source,
assertion, memory, settings, foreground and logging owners. Its real commit calls
`MemoryService.ProposeGuarded` and rechecks cancellation, captured guards, source
snapshots, parent refs and current feature flags on the SAME writer session before
proposal mutation. It constructs contextual provenance and parent/dependency refs
outside model arguments, invokes the existing native typed-domain proposal, then
calls `Owner.AfterSaved` outside the writer for public review/embedding. The queued
work uses a fresh context, realtime budget lane and model priority 1.

## Application integration still required

This package does not install the foreground `learning.propose` action router,
create domain exposure capabilities, or change live/app/Experience. Engine source
positions are constructor 324-351, action submit 1081, recall 1143/1155,
cancelScene 1274 and dispose 1289. Application must:

- Construct the owner with the deployment's actual locale comparator, after the
  bound learning owner is ready, and expose one Proposals instance per runtime.
- At action entry check original turn eligibility plus identity/workspace/history
  exclusions. Copy only original turn source snapshots (`nil` is unavailable,
  nonnil empty is authoritative empty), exposed memory refs, typed domain refs
  and owner-minted guards. Do not use always-true guards or model-provided refs.
- Supply session-aware read/relation/commitment/knowledge guards. When handed a
  writer session they must validate within it, not reacquire the root writer.
  A turn ending normally must not destroy authority already copied for dispatch.
- Route recall/edit, scene disabling/deletion and disposal to Recall/CancelScene/
  CloseAndWait. Resume on foreground completion; polling also releases queued work.
- Close contextual jobs before learning/model/DB/authority owners. A failed drain
  remains open and retryable; it is not permission to free their dependencies.

The new memory-exposure owner can supply memory refs. Exact relation, commitment
and knowledge exposure capture and the complete foreground router are separate
integration work. A working queue/native commit is not full model-action parity.

## Verification and complexity

`backend/tests/generate-contextual-learning-fixtures.ts` runs the actual source
classes, not a rewritten oracle. The Go differential checks compare results,
source-derived candidate keys, immutable commit payloads, terminal events and
stats, plus source file hashes. Other tests cover concurrent duplicate admission,
single-active execution, physical abort-ignoring drain, scoped recall, bounded
source FIFO retention, panics, reentrant observers, in-flight admission drain,
actual SQLite guarded proposal rollback and native post-save embedding.

Queue identity construction is O(s log s) comparisons and O(total source bytes)
space. Dispatch and remembered lookup/eviction are O(1); cancellation is O(q*s)
in the worst case and compacts pending only once. No quadratic splice loop was
added. Proposal capture is O(payload bytes + p log p + e log e); source checks
are O(s*e) as in the source. Memory visibility preserves the source full visible
list/support-refresh semantics; no silent cap or truncation is introduced.
No HTTP/101, private media or external network implementation is changed.
