# Memory module map

The public `MemoryService` and `Engine` interfaces remain compatible. Module
boundaries separate decisions and authority, not just line counts. No module below
owns a second database transaction coordinator or a second learning queue.

## Assertion writes and lifecycle

- `domain/record-values.ts`: existing scope keys, fingerprints, source normalization
  and record validation. This extraction does not change stored hash formats.
- `storage/assertion-repository.ts`: internal record/proof SQL, reverse source index,
  before-write and derived-projection invalidation hooks. Raw repository reads are
  not model-facing authorization.
- `read/assertion-authority.ts`: provenance, current applicability, historical proof
  qualification and exact reference views. Current and historical operations remain
  separate; temporal/version drift is not privacy erasure.
- `write/source-admission.ts`: source, subject and parent authority at write time.
- `write/reconciliation-policy.ts`: local duplicate/correction/merge eligibility.
- `write/proposal-command.ts`: the single proposal transaction, assertion write and
  existing checkpoint before typed relation/commitment admission.
- `write/manual-commands.ts`: authorized manual creation, reviewed updates, guarded
  CAS and public promotion. Locking alone does not turn a learned assertion manual.
- `lifecycle/assertion-lifecycle.ts`: source recall, terminal deletion and recovery
  cleanup ordering, reusing the existing two-phase `ProofInvalidation`.
- `lifecycle/commitment-privacy.ts`: transient exact pre-recall references and narrowly
  permitted surviving-proof requalification. It is not a latest-version fallback.
- `src/memory.ts`: constructs this dependency graph once and retains public read,
  search, projection and compatibility entry points.

`MemoryRevisionCoordinator` remains the sole owner of the assertion transaction's
dirty set, history checkpoints and post-commit projection flush. Independent recovery
intents retain their original ordering. A bounded proof traversal failure remains an
error/rollback, never evidence that a record is false or should be purged.

## Source-only learning stages

- `learning/coordinator.ts`: pending/active/retry/failure state, Admission tickets,
  timers, cancellation, settings changes and administrative retry policy.
- `learning/batch-contracts.ts`: internal batch data and narrow stage ports.
- `learning/batch-runner.ts`: compose one batch inside the caller's existing
  flow/priority/budget context; no retries or queue ownership.
- `learning/extraction.ts`: initial extraction and the existing bounded empty review.
- `learning/repair.ts`: the shared optional extra pass using original cited sources;
  content repair cannot rewrite source/subject authority or retain stale typed fields.
- `learning/reconciliation.ts`: choose from authorized targets, without changing the
  candidate's body, sources, subject or time semantics.
- `learning/persistence.ts`: habit-first writes, effect counts and the conservative
  write-intent barrier before any write await.

One `ContextProjection` is shared across all stages in a batch. Extraction and repair
do not receive old memory content. Reconciliation target selection is not authority
to rewrite a candidate. The `applying` barrier means a write may have happened, not
that a saved-count increment has been observed. Partial/unknown writes remain
non-replayable; stage extraction does not introduce another retry allowance.

## Characterization checks

New tests preserve normalized before/after database and authority traces and complete
model prompts/tool schemas, source IDs, write order, queue results and conservative
partial outcomes. Generated identifiers use stable one-to-one mappings where needed;
versions, epochs, proof structure and source semantics are not normalized away.
Existing lifecycle, history, recovery, budget, attribution, relation, commitment and
queue regressions remain unchanged. Final full-gate results are recorded per frozen
checkpoint, not inferred from smaller modules.

This refactor changes no public schema, prompt, feature default, truth policy,
retention rule or learning/model-call count. It does not itself implement salience,
semantic consolidation or better model behavior; see [current status](memory-current-status.md).

## Accessibility and deterministic maintenance

- `working-set/contracts.ts`, `policy.ts`, `storage.ts`, `service.ts`: bounded soft
  metadata and a separate explicitly versioned pin policy. Pins never grant read
  authority and are not disposable access counters.
- `working-set/mutation-hooks.ts`: caller-owned transaction hooks; no independent
  transaction, model call or chat replay.
- `working-set/maintenance.ts`: coalesced notifications, bounded batches, leases and
  pending-only startup. Empty queues do not run a periodic model loop.
- `working-set/selection-tracker.ts`: only actual delivered evidence retained in the
  final handoff counts; a search hit is not a successful model use.
- `read/workspace-authority.ts`, `memory-workspace.ts`, `workspace-exposure.ts`:
  current authorized snapshots and whole-page delivery. Raw storage is not authority.
- `projections/experience-view.ts`: independent qualified assertions and plan
  declarations, not semantic merging or observed shared reality.

## Ephemeral topic routing

- `src/runtime/topic-admission.ts`: pure finite routing hypotheses from trusted
  ownership and bounded untrusted text cues; no model or scheduling side effects.
- `src/runtime/topic-routing-state.ts`: scene-local deferred IDs, cue provenance and
  acknowledged output ownership; no persistent chat copy.
- `src/runtime/topic-reassignment.ts`: validate the entire model-suggested grouping
  before applying it; never mint another wake for consumed input.
- Runtime remains the sole owner of admission, WAIT generations, transport attempts
  and cancellation. Its integration must not turn history read permission into
  another topic's reply target admission.
