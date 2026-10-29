# Automatic person attribution and source clocks

This slice connects automatic learning to person profiles through explicit subject witnesses. It does not infer a personality or establish the truth of a statement. Offline synthetic model tests verify the contract, isolation, persistence and UI, not extraction accuracy on real conversations.

## Subject contract

Automatic proposals may specify `subject`:

- `self` names a cited source message. The subject is its actual non-bot sender, not a model-supplied nickname.
- `mentioned` names a canonical person, a cited message and its zero-based original mention-part index. All three must match a trusted runtime message. Bot and everyone mentions cannot identify a human subject. Such testimony is a reported claim by default, not an autobiographical fact.
- `unresolved` carries no person ID. The system does not substitute the speaker. A useful qualified scene claim may remain without creating a person profile.

Legacy proposals without `subject` remain accepted only when any supplied person ID is a cited non-bot sender. This identity check applies equally to source-only and contextual proposals. Existing manual records remain readable without a bulk model migration.

A witness proves which identity was referenced, not that the model understood a quotation, joke or grammatical subject. Extraction instructions explicitly exclude quoted, hypothetical and joking speech from self-facts. Their real-world classification still needs the later authorized LLM evaluation.

## Evidence and time

The runtime, not model arguments, supplies normalized mention parts and source clocks. Source speakers remain distinct from memory subjects. Witnesses and source observations are attached to their exact supporting proof: sources and parents within a proof are conjunctive; alternative proofs remain disjunctive.

- `learnedAt`: server commit time.
- `observedAt`: earliest trusted `receivedAt` among currently authorized surviving source proofs. Unknown historical receipt times remain unknown.
- `occurredAt`: asserted event instant, only when the source supports that precision.
- `validFrom` / `validUntil`: asserted applicability interval, not observation or learning time.

New explicit-subject proposals must use offset ISO instants or omit unknown event/validity times. Date-only and relative language must not be converted into fabricated midnight precision. The system does not fill missing event times with now. Historical revisions use their own proof observations rather than the latest record's witnesses.

## Privacy and corrections

Person profiles require exact scene and subject authority. Nickname similarity grants no cross-scene access. Simulation does not create records or witnesses. Turning learning off cancels pending work without replaying old messages on re-enable.

Reported testimony cannot silently strengthen or replace an existing self-statement, including through identical-content deduplication. Recall makes the affected proof unavailable and removes its witness and observation metadata; an independent surviving proof may retain a record. Shared-public projections must not expose private observation times or witness identities.

## Verification boundaries

The owner records final focused and aggregate results in the slice artifact manifest after implementation freezes. Until then, this document describes the intended contract, not a declaration that all checks passed. No real provider credentials, platform sends or paid model calls are required by the offline suite.
