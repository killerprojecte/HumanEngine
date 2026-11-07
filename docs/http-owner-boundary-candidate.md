# HTTP owner boundary candidate

**Locally validated; not published or deployed.** The reversible
assertion-eligibility change closes the normal manual-invalidation/ABA publication
race with real HTTP regressions. Actor reservation removes the measured PENDING
handoff starvation without weakening publication ordering. Automatic legacy
eligibility baseline trusts the configured ready database at normal owner startup,
before Engine creation. Final contract checks, 2,371 tests, runtime/Nuxt typechecks,
production build, and isolated packaged Nitro smoke passed on 2026-11-07. This is
not a Docker/container deployment claim; no Docker daemon was available.

The persistent runtime moves Engine/Store, scheduled work, provider callbacks, and
business API operations to an owner worker. A separate configuration worker projects
GET settings/scenes/models through readonly data handles and verifies publication.
Its additional private authority handle holds a writer reservation without
application data changes. The HTTP host owns
authentication, sessions, transport, and private read-only publication connections.
Literal `HUMANENGINE_DB=:memory:` remains the inline development exception.

The command, ports, mounts, main table layout, environment variables and native
dependencies are unchanged. The separate eligibility event requires reader
capability 11 at normal persistent startup, including an empty baseline; older binaries
then fail closed. The external chain
container format remains 1. The existing build adds `engine-owner.mjs` and
`config-reader.mjs` beside the Nitro entrypoint. Readers never initialize, enroll,
migrate, chmod, decrypt model credentials, or change journal/durability settings.

No new request/response size cap, outstanding-request cap, byte budget, request
deadline, shortened lock-wait allowance, or shutdown grace is introduced. Existing
route/provider/image limits and HTTP defaults remain. This is event-loop isolation,
not a reduction of the owner's CPU work or a guarantee of bounded resource use.

## Publication and overlapping snapshots

Owner responses carry the head observed at their exact synchronous body
construction. Middleware checks the current head against that proof; it cannot
bless an older body using a head obtained after asynchronous serialization. Hono's
intentional empty HEAD response preserves the originating proof. Existing
route-specific guards still apply to earlier asynchronous reads and side effects.

Publication is serialized, without an admission cap, through this fixed protocol:

1. The actor first holds `BEGIN IMMEDIATE` on a private authority reservation
   connection. It then pins and validates a separate readonly authority DELETE
   snapshot and pins its readonly main WAL snapshot. This narrowly scoped order
   performs no owner calls or main-acknowledgement waits. Main WAL readers do not
   wait for the normal writer reservation. RESERVED prevents another authority
   writer entering PENDING while the host acquires SHARED. The actor grants an
   exact ticket only after all snapshots are pinned. Readonly integrity can be
   retained after read rollback; native identity/change checks still invalidate
   evidence on the next snapshot. Writable transaction caching is unchanged.
2. The host tries to acquire its own main then authority read snapshots. Its
   private SQLite handles disable the native busy handler. If acquisition is BUSY, the
   host releases both locks, asks the reader to abandon its ticket, waits for that
   acknowledgement, and only then sleeps asynchronously before trying again.
   This lets a pending writer finish instead of waiting cyclically on the reader.
3. Once both host locks are held, the reader verifies within its already-pinned
   snapshots. It acquires no new locks and waits for no owner operation. For pure
   configuration GETs it also projects and encodes the body in this snapshot.
   It releases its readonly snapshots and then its reservation before replying.
   The host already owns its independent authority SHARED lock at that point.
4. The host matches the verified head, its pinned main gate, authority directory,
   reader capability/state, and DELETE journal mode. Owner replies must also match
   their body-construction head. It queues Node response headers and bytes without
   another await, while holding its own authority lock, then releases both locks.

The reservation handle opens only the existing authority file, without migration,
chmod or a journal-mode change. Requested writable flags alone are insufficient:
SQLite may silently fall back to native readonly. Once during connection setup,
the actor verifies the ordinary identity table and executes a qualified zero-row
UPDATE inside the reservation, requiring zero changed rows and successful
ROLLBACK. This proves native capability for that private handle's lifetime;
ordinary publications do not repeat the schema/DML proof. Settled-file tests show
unchanged bytes, mtime and journal state and no row trigger firing. Native SQLite
hot-journal recovery can still write on this handle. No such handle or recovery
work is added to the HTTP host.

Transactions intentionally span IPC in this restricted protocol. A reader crash
cannot release the host's independently owned lock. A crash before successful
verification prevents publication. Neither a worker's liveness flag nor a
release acknowledgement alone is treated as permission to publish. Response
cleanup remains attached until finish/close/abort, without imposing a queue cap.

Only known host read-acquisition SQLITE_BUSY errors are retried. Verification
errors, output errors, and business operations are never replayed. The retry
sleep schedule and accumulated contention allowance mirror SQLite's existing
busy handler, using the reader connections' original busy_timeout values. Queue
waiting, verification and IPC do not consume a newly invented request deadline.
This is not a bit-for-bit reproduction of SQLite scheduling: the extra handoff,
read-lock acquisition episodes and scheduler overhead can increase total request
latency. Contention can still exhaust the existing allowance and return 503.

Ordinary supported external SQL changes still invalidate integrity verification.
A supported change committed between body construction and publication can make
an owner reply stale. A durable authority intent whose main gate has not caught
up is rejected, not papered over with a saved head. The published recovery trust
boundary for unsupported online raw-file changes is unchanged. Main uses WAL:
a direct SQL change confined to main (including a gate-only quarantine/path/
capability change) can commit after the read snapshots pin. That in-flight read
uses its earlier snapshot; subsequent reads observe the change. The previous
synchronous reader had the same snapshot boundary, but IPC enlarges this interval.
This is not enqueue-time exclusion of arbitrary main-only writes. Normal durable
privacy withdrawals commit authority first and remain excluded by the host lock.
The reversible assertion-eligibility candidate also journals invalidated/quarantined
transitions and exact regrants; its legacy baseline policy is still awaiting a
deployment decision. See `reversible-assertion-eligibility.md`.

## Cancellation, shutdown and unknown outcomes

Caller cancellation immediately rejects its waiter. Canceled queued publication
slots drop their body/transport references without overtaking the active slot.
Active handshakes release host locks in finally; exact-ticket cancellation,
abandonment, worker failure and port closure release all three actor handles.
Uncertain rollback poisons and closes every private connection. Output failure
after a possibly committed mutation warns that the result may be unknown and that
writes must not be automatically retried. A caller abort does not prove a mutation
or external send did not happen. There is no automatic owner restart or replay.

Shutdown aborts publication and reaches worker control channels without waiting
for queued data. Engine/plugin cancellation begins before API cleanup drains,
and Store closes after handlers settle. There is no new termination deadline.
A hung initialization, handler or disposal hook can still keep graceful shutdown
pending, subject to the deployment's existing external process policy. A close
message sent during initialization does not guarantee initialization can be
interrupted. Unexpected exit of a ready owner terminates the host with code 1; ordinary
background JavaScript errors retain Nitro-style sanitized capture-and-report.
See `owner-worker-failure-policy.md`. Startup failure remains cached until
explicit shutdown/restart. Coherent readonly
initialization prevents an old-main/new-authority comparison, but a genuine durable
intent still lacking its main ACK can fail initialization. It is not retried while
holding authority or treated as a valid current snapshot.

## Remaining interference and resource risks

- Live status and every other owner API remain on the owner. They can wait behind
  owner CPU work. Status freshness and field meanings are unchanged; there is no
  cached success pretending to be live. Homepage status/logs/usage and model
  protocol reads therefore do not gain complete page-level isolation.
- Shared SQLite locks still serialize authority withdrawals. Holding the host
  read lock across verification/IPC and the actor reservation can delay writers. A paused host can retain a
  lock longer than usual; no new deadline forcibly releases it. This is the cost
  of preserving withdrawal/publication ordering without a new asynchronous driver.
- Native busy-handler sleeps and full-chain verification are off the host.
  SQLite can still perform internal WAL retry sleeps independently of that handler.
  Short synchronous SQLite/file-system reads, socket queueing, request body assembly, garbage
  collection, process memory, native crashes, and OS CPU quotas remain shared.
  Unbounded large bodies or floods can still exhaust memory. Transferable buffers
  avoid IPC copies but are not streaming or resource isolation.
- Very large settings/scenes/models projections share one read-only actor and
  publication queue. They can delay other publications. No new cap hides this.

## Verification and reproducibility

`node scripts/http-owner-load-prototype.mjs` compares a same-loop runtime and the
worker runtime using 30,000 memory rows and 1,000 authority entries. An independent
client runs four actual memory page requests while polling settings/scenes/models.
`node scripts/http-owner-contention-prototype.mjs` interleaves 100 real durable
head/main-gate writes with configuration HTTP reads and reports writer latency.
Polling frequencies, client concurrency and test watchdogs are experiment settings,
not server limits. Successful 200 latency and failures are reported separately.

Tests exercise the actual actor, host fence, supported SQL tampering, PENDING
writers, worker death, exact-ticket cleanup, output errors, cancellation, startup,
HEAD/body proofs, uncertain mutations, and plugin shutdown ordering. They also
exercise more than 32 accepted requests, a 129 MiB IPC round trip, and shutdown
still pending after twenty seconds. Build and production smoke results must be
reported against the final source tree; a source-loaded test is not a container
run. This environment has no Docker daemon.

## Final local acceptance (2026-11-07)

All 2,371 aggregate tests passed with zero failures, cancellations, or skips.
Independent review also exercised automatic baseline followed by old-backup
restore, preserving negative permission states, record material and history without
recreating absent roots. One old temporal test expected reader 10; it now explicitly
checks the new eligibility event and correctly expects reader 11. No production
behavior was weakened to satisfy it.

The source-free packaged Nitro smoke passed authentication/session/CSRF, config and
owner GET/HEAD, live settings writes, native sharp/sqlite-vec inside the owner,
dynamic local plugin loading, logout, and plugin cleanup with zero-exit SIGTERM.

In the synthetic 30,000-memory-row mixed workload, configuration read p95 was
680.1 ms on the same event loop and 4.02 ms with the isolated owner. All four heavy
memory-page requests and all config requests succeeded in both modes. This is a
controlled experiment, not a production latency guarantee.

With 100 interleaved durable writes, all HTTP/config reads and all writes succeeded
at authority histories of 0, 1,000, 10,000, and 100,000 rows. Read p95 was
3.18/8.83/43.97/351.71 ms; writer p95 was 0.88/0.69/34.74/430.46 ms respectively.
The unauthenticated transport control p95 stayed below 1.55 ms. At 100,000 rows the
experiment skipped 2,638 polling ticks because its own eight-client slots were
occupied; this is not a server cap. Full-chain verification and writer-lock costs
remain real and grow with history. There is no claim that all owner work or the
whole dashboard is isolated, and no new protective limits were introduced.
