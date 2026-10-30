# Evidence coverage is scoped

Two questions must remain separate:

- **Delivery:** Did the complete returned tool result fit into this context?
- **Historical exhaustiveness:** Does that result establish every declaration
  ever made, including superseded, deleted, unavailable or unqueried material?

A complete delivery does not establish historical exhaustiveness. Search is a
current authorized query result; a person lookup is a current authorized person
view; an exact message lookup covers only the requested identifiers. Bounded
message windows are not complete conversation history. Unknown historical
coverage remains unknown even for an empty result or a fully selected page.

The runtime supplies compact, server-owned coverage metadata to Planner and
Replyer. It preserves this metadata when constructing the reply brief, selecting
records, omitting content for budget, and offering the bounded reference-repair
hint. Tool result bodies and model-authored plans cannot promote this metadata
to a proof of complete history. No hidden records or corpus-wide counts are read
to manufacture an exhaustiveness claim.

## What this does not prove

The runtime does not perform general semantic entailment checking of a plan's
free text or the final reply. A real synthetic correction test produced a
Planner strategy claiming that only two declarations existed; Replyer repeated
that claim despite an earlier superseded declaration. The regression retains
that faulty plan and checks that the trusted scope reaches Replyer without
being overwritten. It does **not** prove that a language model will always obey
the scope or that the original semantic defect has passed a new live test.

No new model-authored coverage-claim schema or keyword blacklist is introduced.
Mechanical tests cover metadata provenance, projection and omission. Semantic
accuracy and completeness claims still require separate evaluation.
