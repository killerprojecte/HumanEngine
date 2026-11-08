# HTTP publication handoff and slow clients

This change is confined to the existing authenticated configuration GET/HEAD
preview and the final validation in `recovery.Authority.Publish`. It does not
complete the remaining API migration or change route, schema, authentication,
header, durability, recovery, or resource-admission policy.

## TypeScript contract inspected

`src/server/publication-gate.ts:PublicationGate.publishOne` pins its host main
WAL snapshot and authority DELETE-journal snapshot, verifies the worker result,
checks cancellation, and compares the verified observed head with the pinned
gate. It rejects a destroyed/ended target. It then calls `writeHead` and
`end(body)` synchronously, with no await between the final check and enqueue.
Only afterward does `finally` release the snapshots. `end` accepts output into
Node's response/socket buffers; it does **not** wait for a slow peer to consume
all bytes. An authority withdrawal cannot commit before that acceptance but
may commit before the client receives the accepted output. Subsequent response
publication needs a new proof. An unlock failure occurs after output was already
accepted and cannot replace it with a different response.

The relevant existing source tests are:

- `tests/readonly-publication-snapshots.test.ts`: authority reservation before
  main pin, real concurrent writer/head-body coherence, poisoned handles.
- `tests/config-publication-actor.test.ts`: exact ticket consumption, snapshot
  checks, cancellation, bad gates/projections and history integrity.
- `tests/api-publication-proof.test.ts`: exact GET/HEAD ownership, stale-body
  rejection, shared mutable bytes and already-ended targets.

## Go transport difference

Go's `http.ResponseWriter.Write` can block inside the request handler when TCP
buffers fill. `WriteHeader` can also reach a transport for some response writer
implementations. Neither belongs in `Authority.Publish`'s guarded callback.
Moving a plain unowned slice to a later arbitrary write would be insufficient:
there must be a defined irrevocable publication point and a single transport
owner, not a reusable read permission that can re-read/re-bless stale data.

`publicationResponse` supplies that point. A fresh instance belongs exclusively
to one authenticated request handler. Its status is always 200, its header map
is privately copied before reservation, and its HEAD behavior is fixed. Its
`enqueue` callback:

1. Rejects already accepted/consumed instances and canceled requests.
2. Copies the complete projection into an immutable string, or retains no body
   for HEAD after the identical projection validation has completed.
3. Checks cancellation again after copying, then transitions to accepted.

No transport, database access, callback to another worker, channel wait, new
queue, or goroutine is involved in that transition. Memory is proportional to
the full requested response, with no new cap, truncation, or timeout. The new
string copy is explicit ownership protection, not a claim of zero-copy output.

After `Publish` returns (including all deferred cleanup), the same handler
consumes that accepted response once. It clears retained body storage, checks
whether the request was canceled, and only then calls the transport. A canceled,
failed, or partial drain is consumed and is never replayed or replaced by 503.
The accepted flag remains distinct from whether headers were started, so
observability does not confuse a publication with physical delivery.

## Linearization argument

Let P be the transition to accepted, U release of the authority reservation,
R a withdrawal's committed authority intent, and D the client's eventual receipt.

- Authentication and route admission precede projection and P. A request does
  not reuse another request's response object or publication proof.
- The authority mutex and BEGIN IMMEDIATE reserve the authority before main's
  WAL view is pinned. The verified head equals that view's gate. P is inside
  that same reservation, so P precedes U and no R can occur between the head
  check and P. The read callback cannot reacquire/reserialize later for drain.
- The final check confirms both owned transactions remain active, rereads the
  gate in the **same pinned WAL view**, compares the same head, and checks
  cancellation before invoking enqueue. It adds no latest-state re-read and
  does not reject an ordinary later main WAL commit. This is the Go counterpart
  of the TS final pinned-gate/observed-head check, not a new freshness policy.
- If R precedes reservation, a main ACK mismatch prevents P. If P precedes R,
  the already accepted immutable response remains eligible for drain even when
  R precedes D. This is exactly the distinction between Node's synchronous end
  enqueue and its asynchronous network delivery.
- Cancellation before P prevents acceptance. Cancellation concurrent with P can
  be ordered on either side of that final check. Cancellation after P may
  discard output, just as a destroyed Node connection can discard queued bytes;
  it cannot produce a different response or a new publication.
- A cleanup error after P cannot revoke P. The handler still drains the accepted
  response and records the error. Recovery discards a connection whose native
  rollback state cannot be proved. A poisoned dedicated authority connection
  fails later publications; a poisoned main connection is never returned for
  reuse, though its pool may open a clean replacement. No retry is introduced.
- A failure before P cannot reach drain. Uncanceled requests keep the existing
  generic 503 response, with none of the projection bytes exposed.
- All network draining occurs after U, the main connection return/discard, and
  release of the authority mutex. A slow client therefore cannot retain those
  resources. Normal projection computation/serialization still needs its
  consistent snapshot; this is not a claim that all HTTP work is lock-free.

The handler owns a fresh response writer and has not written response status or
body before this handoff. Unlike TS's generic publisher, this private object
cannot be handed an arbitrary already-ended Node response. Go's request context
is the disconnection/cancellation signal. HTTP status/header semantics and HEAD
body suppression are unchanged.

## Boundary tests added

`internal/httpapi/publication_test.go` includes:

- Immutable body/header ownership and rejected second enqueue/drain.
- Cancellation before acceptance and before drain, plus HEAD with no body write.
- Short/partial transport errors without retries, followed by authority writes.
- A real TCP HTTP server/client with a deterministic first-write barrier below
  net/http buffering. It uses a response larger than that buffer, pauses the
  client, commits a real writer through the same coordinator (main mutation and
  ACK included), then commits a revocation through another coordinator. A fresh
  request returns 503 while the paused accepted response drains its exact old
  bytes once released. The barrier avoids kernel-buffer/scheduling timing
  assumptions; it does not claim to measure real-world network throughput.
- An actual SQLite authorizer-denied main ROLLBACK after acceptance: both the
  low-level handoff and complete HTTP handler deliver the committed response,
  no error body is appended, and the poisoned connection is replaced.
- A failed projection that never accepts or leaks its partial source data.

`internal/recovery/publication_final_test.go` includes post-projection
cancellation, lost snapshots, changed gates, a later main WAL commit that leaves
this publication's pinned view valid, and an actual authorizer-denied authority
ROLLBACK that preserves one acceptance and prevents reuse.

Tests use finite failure guards only to detect test hangs. No production limit
or timeout was added. Full repo acceptance requirements remain in `AGENTS.md` and
`backend/AGENTS.md`; passing these focused tests is not a full migration claim.

## Validation correction

The first focused race run reached the new TCP scenario but rejected its writer
before lock acquisition with `recovery_authority_not_independent`. The reused
HTTP-only fixture had `root/main.sqlite` and `root/authority/authority.sqlite`.
The production writer correctly rejects authority nested within main's directory.
This was a fixture failure, not evidence of slow-client lock blocking. The shared
fixture now uses independent `root/live/main.sqlite` and `root/authority` paths
and checks `recovery.AuthorityPath` during setup. No production policy or code was
relaxed. The writer still uses `OpenWriter`, shared with the HTTP handler, and the
separate revocation writer remains independently opened. Failure diagnostics now
report the failed concurrent operation without attributing every error to locks.
The corrected candidate needs the focused race run repeated.

## Integrated verification

Independent source/code review found no remaining blocker. Three affected
packages (httpapi, recovery and candidateservice) passed race and vet after
correcting the synthetic fixture layout and providing its source-test dependencies.
The first failure was the existing authority-directory independence rule, not
a slow-client timeout. All real writer, withdrawal and exact-response assertions
remain. Full-project exact-head CI for this change is pending.
