# Bounded asynchronous log delivery

Normal `Store.log()` admission no longer performs file writes, rotation, or stdout
writes on the runtime thread. A process-shared stdout worker and a separate worker
per canonical log directory own delivery. Stores sharing a directory share its
owner; shutdown cannot overlap an old rotating writer with a replacement. Initial
canonical-path resolution occurs once per acquired owner, not for every record.

This is **bounded best-effort delivery**, not a lossless audit ledger. Normal
admission never waits for a slow sink. File delivery allows 4 MiB / 1,024 queued
and in-flight records; stdout allows 1 MiB / 256. One eighth of each budget is
reserved for warning/error admission, within—not in addition to—the total cap.
Oversized records and overload are rejected whole. Counters distinguish rejected,
failed, pending, written, and unconfirmed records. An uncertain partial write is
never automatically replayed. A failed sink remains degraded rather than silently
restarting and implying complete delivery. Transient stdout `EAGAIN` is different:
that syscall wrote no bytes, so the worker retries only its unwritten suffix with
capped backoff. The `backpressured` flag and pending counts remain visible. Shutdown
notifies the shared stop flag; ambiguous write errors never replay a prefix.

Same-stack records share a bounded worker message, but retain individual FIFO and
rotation boundaries. Admission reserves credits before exact unpooled UTF-8
allocation. Flush controls first dispatch all admitted records and await an atomic
acknowledgement without polling or blocking the main thread. Native Node worker
flag inheritance is preserved; immutable module-neutral source requires no flag
stripping or executable caller interpolation.

The in-memory log view retains at most 500 whole records and 4 MiB of encoded
record bytes. Evictions and oversized rows have separate counters; `log()` still
returns its original row. These are logical encoded-byte limits, not a claim that
all JavaScript object memory is bounded by exactly that amount. Formatting and JSON
serialization still execute on the calling thread; worker isolation is not itself
a claim of lower total CPU consumption.

`GET /api/logs/status` uses the same authentication as the other administration
routes. The Logs page displays degraded delivery. A sanitized, rate-limited warning
is retained in memory without recursively writing to the failing sink. No record
contents, paths, or exception messages are included in delivery health.

File workers retain an open descriptor, rotate whole JSONL records at the existing
2 MiB threshold with two backups, and preserve an interrupted trailing record in
a rotated file. Explicit flush barriers acknowledge all records accepted before
that call and fsync the file. A later concurrent flush includes its own newer
records. `complete` and `durable` remain false if any loss/failure is known; stdout
can be complete but is never reported durable.

Graceful server shutdown awaits asynchronous drain and file fsync, with a five
second deadline. Legacy synchronous `Store.close()` retains a bounded shutdown-only
barrier; logging itself never uses that barrier. A timeout is reported incomplete,
with unconfirmed records accounted for. Worker termination is requested, but an
operating-system blocked native operation cannot be promised an instantaneous
interrupt. No new file owner may overlap that still-unclosed owner. Abrupt process
termination can lose queued records, so applications must use graceful shutdown
when diagnostic persistence matters.

See [matched workload validation](logging-performance-validation.md) for measured
CPU, latency, RSS and saturation results. The accepted architecture tradeoff is
bounded, observable delivery and isolation from slow sinks—not a universal CPU
reduction or lossless overload guarantee.
