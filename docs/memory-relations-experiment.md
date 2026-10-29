# Scoped relation learning experiment

This slice adds an opt-in, source-backed `participates_in` relation path. It does not
establish that an assertion is true, infer consent or permissions, or demonstrate
model quality or cost savings. The ordinary memory path remains the default.

## Controls and autonomous admission

All four controls default off: `memoryGraphEnabled`,
`memoryGraphAutoAdmissionEnabled`, `memoryGraphAnswerEnabled`, and
`memoryGraphShadowEnabled`. Enabling the graph does not enable scene learning.
Scene learning must already be enabled for automatic intake. The same existing
learning proposal may include up to three relations; no extra extraction model
request is created. With automatic admission enabled, only the deliberately narrow
self/explicit, positive, asserted, current, unconditional case can be admitted.
Other valid relations remain qualified candidates; administrators may accept or
reject them with an exact revision check. This is not a mandatory human review
for every low-risk observation.

`accepted` means admitted to this index, **not verified truth**. A reason saying
there was no visible conflict refers only to the currently authorized bounded
records inspected. It is not evidence that no contradictory statement exists.

## Identity and qualification

A relation has an authorized person subject, an explicit plan/event entity,
polarity, modality (`asserted` or `possible`), temporal scope (`current`, `past`,
`future`, or `unspecified`), complete conditions, and optional precise event and
validity times. Unknown historical dates stay unknown; `past` does not manufacture
a midnight timestamp. Negative, possible, conditional and historical relations
are not silently turned into current positive facts.

New entity IDs are server-generated. Equal titles do not merge identities.
Reusing an existing entity requires an actually delivered contextual reference
and its exact backing assertion revision/deletion epoch. Source-only extraction
cannot guess existing entities. Existing assertion subject, scene and provenance
restrictions continue to apply; relation pointers do not widen authority.

## Transaction and lifecycle

The assertion and its final proof/revision are checkpointed before relation
admission, in the same SQLite transaction. Invalid relation proposals do not
invalidate an otherwise independently valid assertion. A failed transaction cannot
leave a detached relation. Records preserve complete proof alternatives (OR) and
every dependency within an alternative (AND).

Assertion revision changes invalidate the associated relation bodies and entity
pointers, including reverse target references. Recall and deletion remove the
corresponding stored relation/entity bodies synchronously; minimal relation ID and
version tombstones remain. Disabling a feature makes its model capability
unavailable without pretending all stored data was deleted. This is logical
application deletion, not forensic erasure of prior backups or storage pages.

## Planner and Replyer

When graph answering is enabled, `memory.relations` supplies bounded, scene-scoped
complete assertion records plus qualified relation evidence. Candidate
counterclaims are retained alongside accepted observations for the same entity;
status and time remain explicit. The current store inspects at most 201 scene rows and returns at most 40 relations;
more than 200 scene rows explicitly limits even person-filtered reads. This is not
paginated complete-graph retrieval. Overflow requires a narrower supported scope rather than
silently returning a supposedly complete prefix. The read does not issue a model
or embedding request.

Preparation is not exposure. Only unmodified records actually delivered after
Planner evidence budgeting register source dependencies and entity references.
The complete relation, source assertion and target assertion form a required
selection group. Same-entity counterclaims cannot be omitted while keeping only a
positive conclusion. Replyer packing keeps or omits the whole selected receipt,
including polarity, modality, temporal scope, conditions, provenance and time.
Changes to backing revisions, relation review status or feature flags invalidate
an in-flight exposed snapshot. Queued contextual learning which actually received
graph evidence retains an independent edge-review validator through commit;
normal foreground turn completion does not cancel that job. Ordinary memory
search/person reads do not acquire graph-edge dependencies and are not invalidated
merely by a relation review action. Rejecting an edge does not delete its underlying
assertion or unrelated ordinary memories.

Shadow mode only compares bounded authorized ID-set cardinalities after ordinary
memory search. It neither substitutes graph answers nor adds model calls. Errors
are isolated from baseline retrieval. Counts are not relevance, truth, quality,
token or cost measurements.

## Validation scope and remaining work

Tests use synthetic source messages, temporary SQLite databases and scripted or
loopback models. They verify schemas, transactions, source authority, lifecycle,
conditional handoff, cancellation, administration and feature gates—not semantic
extraction accuracy. Actual model evaluation still needs the user's later API and
scenario review. This is a limited plan/event relation vocabulary, not a complete
human-like knowledge graph. Outcome-grounded strategy learning, broader relation
vocabularies and measured retrieval improvements remain separate slices.

## Offline checkpoint (2026-10-28)

The final slice passed 855 tests, generated-contract verification, runtime and
Nuxt type checks, formatting, and a production build. An independent boundary
review covered exposure, revocation, queue receipts and qualification closure.
A production-page synthetic fixture verified review cancel/accept/reject, reload,
scene switching, independent settings persistence, total OFF and logout cleanup.
No provider model or real platform send was used. These results establish the
tested engineering contracts, not semantic extraction accuracy or efficiency.
