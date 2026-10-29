# Optional working set and experience views

This slice adds deterministic accessibility metadata and read projections. It does not add a language-model consolidation job, infer that frequently selected information is true, or synthesize several claims into a new real-world event.

Both `memoryWorkingSetEnabled` and `memoryEpisodesEnabled` default to false. Enabling either activates their shared accessibility metadata and bounded maintenance; each switch independently controls its own view. Both must be off to stop the shared metadata work. Existing scoped search, person and historical reads remain the baseline and can retrieve authorized cold records. Cold is not deleted, false, unavailable or forgotten. Unknown observation/occurrence times remain unknown.

## Bounded reads and evidence

`memory.workingSet` and `memory.episodes` expose whole qualified pages through the existing prepare/deliver boundary. Reply plans select complete pages with `stateEvidenceIds`. An omitted or limited page is not evidence of absence. The final Replyer payload retains full record qualifications, plan conditions, participant declarations and coverage, or omits the complete group. Access metadata is not an execution permission.

The working set protects open and needs-review plans and their necessary currently authorized assertion evidence. When bounded plan coverage cannot establish whether a record is protected, the result is explicitly limited and conservatively avoids cooling it. Candidate and page limits are not a complete-library ranking or a claim of improved retrieval accuracy.

Experience views keep each qualified experience assertion separate. A shared-plan entry reports participants' own declarations, including self-reported completion; it does not say the system observed a shared event. Similar titles, dates or locations never merge records or establish participation, feelings or causes. Plan version recording time is not event time or a complete historical confirmation snapshot.

Reading these state tools does not authorize free-form derived learning in the same turn. A subsequent independent source-only learning operation remains available under its original source and subject rules.

## Metadata, dirty work and measurement

Metadata uses exact assertion version/epoch or plan mutation references plus current proof/source qualification keys. Deterministic dirty processing is bounded and has no model or network port. Old leases cannot commit over a newer generation. Feature disablement cancels pending metadata maintenance; restart does not replay old chat into a model.

`context_selected` means an already-authorized complete assertion body survived final context packing. It is not a retrieval hit, model transmission, successful use, user approval or reward. Administrator reads, background rebuilding, omitted records and simulation do not add this count. Plan backing records not actually displayed are not counted as selected assertion bodies. Counts and clocks do not change truth, confidence, epistemic class, support or content.

## Explicit pin policy

Pin intent is distinct from disposable access statistics and from `locked` content protection. Pin commands use an exact qualification anchor and independent policy revision CAS. The independent recovery authority records policy changes before local commit. Pinning cannot grant access to an otherwise unavailable source and does not promise permanent retention of original chat.

A later version/source qualification change retains the original pin intent as stale/unknown and conservatively protects an otherwise authorized entity from cooling. It does not silently rebind the pin to new content. Administrators may explicitly re-pin or clear a stale policy, including while the working-set feature is disabled. The metadata-only policy list does not expose the old assertion body or claim its current availability.

Recovery resets disposable statistics, receipts and dirty work, not pin policy. It folds the latest ordered pin/unpin/terminal policy. An existing entity with later qualification retains stale intent; a backup missing the required entity/version material is quarantined rather than silently dropping the pin. Terminal deletion clears the corresponding policy. Source recall or ordinary metadata invalidation is not itself terminal deletion and must preserve independent legal support.

## Management

Authenticated routes are `/api/memory-working-set`, `/api/memory-working-set/pin`, `/api/memory-working-set/rebuild`, and `/api/memory-episodes`. The rebuild action refreshes only the bounded current candidates, not the entire database. UI coverage and unavailable-source states must remain visible.

No real-model quality, token savings, semantic consolidation benefit or production latency claim follows from deterministic fixtures. General episode synthesis and learned semantic consolidation remain separate experimental work; these views do not implement them implicitly.
