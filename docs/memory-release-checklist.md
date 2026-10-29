# Memory redesign: final release and test handoff

## What is implemented

The finite twenty-slice redesign connects source-qualified learning and retrieval,
version/epoch authority, safe recovery, explicit source edits, multi-person plans,
qualified history, selective working sets, deterministic dirty maintenance, topic
admission, independent read/personalization controls, verified identity links,
bounded structured knowledge, incremental attributed summaries/episodes, three
low-risk operation trials, recorded plan timelines and declared external costs.
These are executable modules with API/UI and synthetic Runtime coverage, not claims
that extracted statements are true or that model behavior improved.

Experimental graph, identity, semantic and feedback features retain their individual
controls and scope restrictions. Identity links do not merge permissions. Reports of
helpfulness/completion are user declarations, not observed outcomes. Source withdrawal
and authorization still dominate historical and derived views.

## Integrated offline acceptance

The executable instructions are in [the safety runner](acceptance/safety-runner.md).
The frozen manifest binds 200 distinct named test cases to exact fixture hashes.
Skipped, TODO, missing or failed cases cannot count as passed. The separate actual
Engine/Runtime longitudinal runner uses three genuinely different configurations:
no memory, ordinary memory, and qualified features. It drives the same synthetic
multi-day event sequence through learning, reads, edits, independent evidence,
18-to-19 plan amendments, withdrawal and restart. Event dates never reset the actual
billing clock. Its output is mechanical evidence, not semantic gold or a model score.

The final owner test run passed 1,631/1,631, with generated contracts, runtime/Nuxt types,
production build and formatting also passing. Clean-source inspection and bounded
performance are separately recorded gates. A report marked
passed by one runner does not substitute for them or prove publication occurred.

## Migration and rollback

Follow [the migration checklist](memory-migration-checklist.md) on an isolated test
installation before enabling real adapters. Preserve the independent authority and
accounting/session ledgers separately from snapshots. Do not downgrade a reader after
new authority events, enroll an old backup as current, or remove a restore HOLD to
pretend missing usage was reconciled. Unknown external costs survive calendar changes.
Registered local backup expiry is explicit and dry-run by default; it is not a promise
of deleting uncontrolled third-party copies.

## Still unverified with real models

Real provider authentication/usage/cache framing, calibrated token accounting,
semantic extraction accuracy, quotation/joke/negation handling, summary fidelity,
necessary-evidence recall, multi-party understanding, long-term task benefit and
production cost/latency remain unmeasured. Synthetic scripted responses cannot answer
those questions. Independent human gold review remains pending.

No real API credential is included in source or reports. Use the separately authorized
[API preparation path](memory-evaluation-api-preparation.md) only after endpoint,
model and request/time/output/spending scope is approved. Request-bounded mode is not
a strict dollar guarantee; strict mode refuses unsupported contracts. Source-publication
permission is independent of permission to transmit credentials or spend money.

## Publication record

Publish only the tested clean source to the approved repository and branch, preserving
unrelated remote changes. Recheck the head, use a guarded non-force ref update, and
verify the resulting commit and CI. Exclude credentials, live databases, raw chat,
media, local reports, screenshots and dependency/build directories. A private source
archive is a recovery checkpoint, not evidence that the public branch was updated.

## Bounded performance sample

On the final source, the unchanged synthetic 2,000-record read workload completed in
13.356 seconds with sampled peak RSS 590,236 KiB. The guard limits remained
800 MiB RSS, 256 MiB V8 heap, 75 CPU seconds and 90 wall seconds. The earlier
post-fix sample was 11.746 seconds / 554,668 KiB. These isolated runs show completion
within the same engineering bound, not controlled production latency or a speedup.
Earlier failed/terminated receipts are retained, not relabelled successful.
