# Exact premise versions and bounded evidence inspection

This checkpoint binds new automatic contextual proofs to the exact premise versions
actually exposed at an authorized read boundary. It does not prove that a premise
is true, implement unrestricted time travel, or establish real-model quality.

## Write authority

New contextual writes carry trusted `{id, version, deletionEpoch}` references with
an exact, unique match to their parent ID set. Models cannot provide these fields.
The Engine fixes the first exposed snapshot within a turn; a changed second read
makes that generation stale rather than silently replacing its authority. Queued
learning retains those references and checks them again in the authoritative write
transaction. Source-only extraction has no parent references.

A legacy record may receive its first revision at a real, authorized read boundary.
Its explicitly stored dates and unknown provenance are preserved. Simulation never
performs this migration. Existing ID-only proofs remain qualified `legacy_unknown`;
no migration guesses which old premise version was originally read. Compatibility
fixtures deliberately retain that old persisted format, while separate integration
tests exercise strict new writes.

## Current answers versus retained history

Each proof remains AND across its required sources and parents; alternatives are OR.
Changing a bound premise revision makes that proof unavailable for current answers,
even if only metadata changed. It never automatically rebinds to the new premise.
An independently supported alternative can still keep the current assertion usable.

Version drift, expiry and supersession are not themselves privacy deletions. Exact
old revisions may remain inspectable when their original sources, current scope and
deletion epoch still authorize them. Recall, deletion, changed epochs and revoked
scope remain hard barriers. Current and historical reverse dependencies participate
in body and shadow-index removal; an OR survivor cannot revive descendants tied to
its old epoch. Legacy replacement behavior remains conservative.

The authenticated history endpoint deliberately separates history inspection from
current-answer applicability. It requires a surviving current assertion in the exact
requested scene and rechecks each historical revision's own scope and complete proof.
An expired or version-stale assertion can therefore have inspectable retained history
without becoming a current answer. Neither shared historical scope nor a safe current
OR alternative authorizes an unsafe older revision. This is a deliberate extension
of the earlier current-list-only history gate, not an unrestricted admin dump.

The endpoint returns at most 200 latest retained versions in ascending order and
reports limited coverage when earlier versions are omitted. It exposes a human-readable
premise-binding qualification, not raw proof IDs/epochs. The Replyer brief preserves
legacy-unknown qualification without receiving a database graph.

## Resource bounds and modularity

`memory/domain/proofs.ts` owns proof validation and canonical keys.
`memory/read/proof-parents.ts`, `bound-parents.ts`, `provenance.ts` and
`applicability.ts` separate version, historical authorization, source provenance and
current time/status applicability. `memory/lifecycle/proof-invalidation.ts` owns
reverse traversal and invalidation orchestration through narrow ports; the facade
retains transaction authority, not a second state machine.

Proof traversal uses bounded depth and work counters. Provenance budgets apply per
root, not to the entire database size; a history inspection shares its own traversal
budget. Only proven-success memo entries are reused, so an OR cycle/path cannot poison
an independent valid route. Exhaustion produces an explicit bounded error, not an
empty-complete result or a declaration that evidence is invalid. Lifecycle transactions
roll back on that error, including any earlier attempted purge in the same transaction.

## Compatibility and limits

Old API records and IDs remain readable, but new trusted contextual proposal calls
must supply exact parent references. Deletion and recall do not become optional.
Readers predating bound proofs must not be used as a rollback strategy that silently
interprets those proofs as latest-ID dependencies. Restoring a stale database still
requires reconciliation with the latest deletion state; this checkpoint does not
provide an external deletion ledger.

Tests use synthetic SQLite data, local adapters and deterministic model receipts.
They do not measure human-like cognition, semantic accuracy, token savings or provider
billing. Budget enforcement, graph expansion and strategy experiments are separate
checkpoints and are not claimed by this parent-version slice.
