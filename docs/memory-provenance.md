# Memory provenance and recall safety

## Trusted extraction boundary

`MemoryService.propose` requires a trusted `MemoryContext.provenance`:

- `source-only`: content is extracted without exposing related memories; dependencies must be empty.
- `contextual`: the caller must provide all memory IDs exposed to the generating model in `dependencyMemoryIds`. An absent dependency array is rejected. These fields are not accepted from model candidate arguments.

Background extraction reads current messages first. A separate reconciliation pass may associate immutable candidates with existing records, but cannot enrich their content or change their source IDs. Runtime learning conservatively depends on all memory records actually exposed during its turn. Missing, unavailable, cross-scene/cross-person and cyclic dependencies are rejected before writing.

## Proof semantics

SQLite `memory_supports` stores alternative proofs for a record. Every proof requires **all** its source messages and **all** its parent memories. A record remains supported if **any** complete proof remains supported. Separate independently extracted exact-content corroborations add alternative proofs. A single multi-message extraction is not assumed to establish that each message independently supports every fact.

Parents are checked transitively. Recalling a required source, superseding a premise, deleting or rewriting a premise, or expiry removes unsupported descendants from retrieval and vector indexing. Locks and metadata edits do not override missing evidence. Proof history is retained when a premise is replaced/deleted; invalidated records retain their content and surviving source identifiers for administration. Recall, proof updates, supersession and vector cleanup are transactional.

Fresh source-only corrections do **not** depend on the obsolete preference they supersede. Therefore recalling the old preference does not erase the independent correction.

## Conservative limitations

A semantic paraphrase with different normalized content is not treated as an independent proof of an older potentially richer record. Source-only semantic merges are rejected; the engine keeps the immutable extracted candidate separately. Exact-content deduplication remains supported. This trades additional paraphrase records for avoiding unsupported fact inheritance. Search remains bounded by its existing result limit.

Single multi-message summaries may be withdrawn after one contributing source is recalled, even when an unavailable semantic verifier might have proved that another source was sufficient. Independent exact-content extraction proposals remain alternative proofs and survive loss of other alternatives.

## Legacy migration and manual review

Existing learned records have no trustworthy historical dependency graph. Migration atomically marks them `lineageStatus: unverified`; it preserves their content, identifiers, sources, confidence and status. It never invents independent provenance or replays historical messages.

A subsequent recall in a scene conservatively quarantines its unverified records, including all person scopes, plus unverified public records with a source from that scene. This scene-wide reach is intentional: historical related-memory retrieval could expose any person record in the scene. Other scenes and manually authored records without inherited lineage are unaffected. Public records with no remaining source-scene evidence cannot have historical dependencies reconstructed automatically.

`quarantined` records are excluded from ordinary and simulation retrieval and indexing, but remain visible in administration. This is a reversible, nondestructive safety restriction, not a claim that every quarantined fact was false. Changing locks, importance, confidence, kind or status does not clear quarantine. Explicitly submitting reviewed `content` (even unchanged) turns the record into manually owned content and clears inherited lineage. If its status is already invalidated, an explicit manual active status is also needed to make it searchable.

After restart, persisted proof alternatives remain authoritative. Expiry is checked at read/index time; normal reads remove unsupported vectors, while simulations filter unsupported records before top-k ranking without writing anything.

## Regression evidence

`tests/memory-lineage.test.ts` covers multi-level recall, alternatives, independent corrections, scope, missing/cyclic parents, locks, metadata edits, public promotion, restart, conservative migration, migration/commit atomicity, expiry and simulation read-only top-k behavior. `tests/memory-reversion.test.ts` preserves the A→B→A fresh-evidence safeguards.

The real report defect was replayed offline against a backup of the sealed learned database with its original record IDs. Before recall both the release-code record and borrowed joke summary were searchable. After recalling all six original code sources, neither was searchable; the derived legacy summary and its two own sources remained available for review as quarantined. The original database and sidecar hashes were unchanged. No model requests or credentials were used.

## Bounded correction and confirmation coverage

Reconciliation is not dependent on successful embedding coverage. After source-only extraction, trusted current-scene evidence authorizes a narrow metadata lookup: a personal candidate must cite a current live message from that stable person ID; an unscoped candidate does not gain access to person-scoped records. Fact/claim targets form one compatible statement group; other kinds retain their own group. Cross-scene, expired, recalled and quarantined records are excluded. Manual and locked records may be visible for comparison but are never revisable by the learner.

The metadata lookup inspects at most the newest eight eligible rows per scene/person/kind group, with one additional row used only to detect truncation. Up to four targets per candidate and eight per group are selected, then deduplicated to at most sixteen total targets, incorporating up to eight semantic hits. Two metadata positions are reserved before semantic hits so an unavailable vector cannot entirely remove the metadata path. A small lexical tie-break within this bounded set is relevance only, not authority. These limits can still omit an older relevant record; `targetCoverage: capped`, explicit limits, and `learning_reconciliation_targets_capped` make that tradeoff observable. Missing targets never prove absence. Each target declares the candidate indexes allowed to use it.

Comparison includes existing `occurredAt`, `learnedAt`, `validUntil`, current time and timezone. Event time and ingestion time are distinct. Neither recency alone nor an older event timestamp automatically authorizes or forbids supersession; original source-only candidate dates remain immutable.

An initially empty extraction can receive **one** source-only review when available live input contains a narrow explicit correction or independent-confirmation signal. Only up to eight qualifying original messages are included, with no old/related memories, and up to eight candidates can be returned. Ordinary chatter, quotations and hypothetical confirmations do not trigger this review; silence remains a valid result and no transcript/fabricated candidate is substituted. The review shares the single extra-pass budget with neutral pronoun repair: its output cannot trigger another repair pass. Thus each queue attempt makes at most extraction + one extra review/repair + reconciliation completion calls, aside from the existing separately guarded public-knowledge review. Bounded precommit transient retries can repeat an attempt; partial writes are never replayed. Review start/completion, rejected outputs and truncation are logged without source text.
