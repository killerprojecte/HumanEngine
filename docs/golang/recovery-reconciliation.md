# Quarantined recovery reconciliation

The Go offline lifecycle now implements the source reconciliation order using an
admitted maintenance owner and an exclusively owned staging connection. It is
not yet wired as a complete production boot path, and does not complete the live
habit, meme or experience-learning API migration.

The runner audits the authority history and original prefix/head, replays suffix
transitions and all-history privacy obligations, and composes actual identity,
assertion, source-event, semantic review, knowledge, commitment, working-set,
external-cost, settings, habit, media, experience and trial owners. Repeated
redaction receipts are counted individually. Missing evidence is not fabricated.

The final proofs, working-set reset, restored-model hold and ready gate update
share one guarded main transaction. That main gate is the ACK; no additional
authority record is created. Committed replay prefixes survive failure and can
be reconciled again. Native transaction loss remains latched even if callers
catch the SQL exception; ordinary caught SQL errors retain source behavior.
Uncertain commits are reported without blind retries.

Reconciliation owns WAL/foreign-key setup and rejects an existing caller
transaction before setup. A direct media deletion also rejects an outer
transaction before unlinking a file. Pure-SQL nested maintenance retains its
savepoint semantics. These are transaction/filesystem correctness boundaries,
not new history, resource or workload caps.

## Evidence and remaining acceptance

The actual TypeScript generators create synthetic databases and media in private
temporary directories. `make differential` regenerates the domain, source and
trial fixtures before using them in the whole-runner generator. Twenty source
scenarios compare SQL rows, media bytes/absence, authority head, reader and gate,
including a nonempty mixed-domain restore, duplicate unlink, final-gate failure,
connection restart and successful repeat.

Private targeted tests and independent code review passed before integration.
The integrated source regeneration and all twelve affected-package race/vet
checks passed. Full-project and native-ICU exact-head CI acceptance are still
pending for this checkpoint. These tests do
not establish universal parity or whole-application readiness.
