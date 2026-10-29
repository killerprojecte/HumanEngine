# Memory implementation plan

Baseline: `ebbdd32472cacc0b05b2e80d552dd67631dba63f`. The approved
2026-10-28 evidence-audited design is a roadmap, not measured production benefit.
All initial verification uses synthetic records and offline model receipts.

For the current implemented/experimental/missing capability inventory, see
[memory-current-status.md](memory-current-status.md). The numbered checkpoints
below are the original rollout plan, not a claim that later capabilities are absent.

## Boundaries and ownership

- `memory/domain`: stable records, candidate contracts and scope types; no model SDK or database.
- `memory/storage`: additive migrations, repositories and transaction/outbox ports; no network calls.
- `memory/read`: lexical/hybrid retrieval and bounded evidence selection.
- `memory/projections`: authorized person/timeline/relation views, never independent facts.
- `memory/write`: attribution, validation and versioned commit commands.
- `memory/lifecycle`: deletion, recall and dependency invalidation.
- `memory/learning`: coalesced extraction admission, reconciliation and task lifecycle.
- `runtime/turns` and `runtime/scheduling`: waiting/generation and resource admission, not semantic truth.
- `src/memory.ts`: compatibility facade until callers can migrate incrementally.

Existing proof alternatives remain OR; every source and parent within an alternative
is AND. `verified` lineage means available provenance, not demonstrated truth.
Unknown identities/times are not inferred during migration. The canonical platform
identity remains internal; nicknames do not merge people across accounts or scenes.

## Deliverable checkpoints

1. **Compatibility and person projection.** Extract domain, SQLite setup and lexical
   code without changing existing write semantics. Add exact-subject, scene-filtered
   person views using the existing authorization path. Timeline describes currently
   visible records, not a complete correction history. `learnedAt` is recorded time,
   never substituted for an unknown occurrence time. No new automatic extraction.
2. **Versioned evidence foundation.** Add nullable explicit subject/time metadata,
   immutable assertion revisions, admin assertion events, expected-version and
   deletion-epoch checks. Preserve existing IDs, support alternatives and tombstones.
   Test restart, legacy restore, stale commits and correction/deletion races before
   exposing new commands. Add authenticated person/source/history views to the UI.
3. **Independent learning and semantic waiting.** Reuse existing received-message
   learning (already independent of replies); do not add duplicate silent extraction.
   Move contextual candidates out of the blocking reply path. Coalesce by authorized
   sources, enforce learning-OFF cancellation and no automatic paused-period catchup.
   Durable outbox initially covers committed projections, not replay of old chats or
   unknown sends. Implement bounded WAIT wakeups/generation checks and foreground
   priority without claiming that a queue can preempt an arbitrary network call.
4. **Budgeted retrieval experiments.** Preserve legacy reads as a baseline; add
   version/scope-checked temporal retrieval and optional bounded relation expansion.
   Shadow evaluation and feature flags precede changing default answer behavior.
5. **Experience and evaluation.** Add source-linked external outcomes and isolated
   candidate validation; strategies never modify authority. Graph/strategy experiments
   remain independently disableable. Real model quality/cost evaluation requires a
   separately supplied authorized API and multi-scenario held-out runs.

Each usable checkpoint gets focused regressions, full contracts/types/tests/build,
independent review and a durable source backup. Publishing a checkpoint does not
mean the roadmap is complete. Backward-compatible rollback must retain current
recall/deletion state, not restore deleted content from an older snapshot.

## First checkpoint limits

Person projections require explicit existing `personId`. Scene-only statements and
shared knowledge are not silently assigned to their source speaker. Simulation
returns no real-person view. Projection functions do not mutate assertions; the
facade's existing `list()` may refresh invalid support state for real scenes.
No claims about token savings, semantic extraction quality or human-like cognition
follow from deterministic tests. No real platform messages or paid model calls are
part of these checkpoints.
