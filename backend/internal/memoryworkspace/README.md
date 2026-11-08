# Scoped workspace snapshots

Resolver composes actual assertion authority and current commitments from a
single callback-scoped `commitments.ReadService`. It retains ordered source-head
occurrences in qualification hashes, exact plan mutation anchors, and the source
open/needs-review protection precedence and coverage rules. It does not grant
access to record bodies or reuse eligibility answers across reads.

Use `ReadCurrent` for an independent publication or an already-bound write child.
Inside an existing working-set publication, use `ReadWorkingSetCurrent` with its
concrete scope. That bridge validates owner identity and borrows native prepared
reads without entering another transaction. Neither resolver nor its service is
concurrent; both expire when the owning callback returns.

The recording-port source oracle isolates the actual TypeScript resolver contract.
Separate native tests use real assertion history, registered source revisions,
commitment storage, child transactions and one-connection publication scopes.

This package does not implement outer MemoryService refresh/capture, workspace
candidate ordering/capture, final model-context identity or runtime delivery.
