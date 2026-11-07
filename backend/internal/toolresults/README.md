# Ephemeral tool-result storage

This package ports `src/tool-results.ts` into Go. It uses private temporary files,
not a TypeScript proxy or an in-memory replacement. Production plugin-manager
composition remains a separate migration step; this package does not change the
REST API, frontend, startup, or global build targets.

## Preserved behavior

- `Store` consumes the complete string, byte source, or stream before returning a
  preview. There is no new download, response, stream, storage, or cache-size cap.
  The existing JS safe-integer byte-count contract is retained.
- The default preview is 4,096 bytes. Only a model view has the existing 32,768
  byte budget. `full` requests the remaining range, not an unbudgeted response.
  Repeated ranges reconstruct the complete stored data without rerunning a tool.
- Budgeting measures `JSON.stringify(JSON.stringify(content))` using `jsjson`.
  It includes escaping and base64 expansion and shrinks byte counts by the
  source's `floor(count * 0.8)` steps. A budget reduction can split UTF-8.
- UTF-8 decoding is fatal, preserves a BOM, and falls back to lossless base64 on
  any invalid or split sequence. Once a view falls back it remains base64 while
  shrinking, even if a shorter prefix would have decoded as UTF-8.
- `Text` takes canonical WTF-8 from `jsjson` and applies Node `Buffer.from` UTF-8
  conversion, replacing each lone UTF-16 surrogate with U+FFFD only at this byte
  boundary. `Result.JSONValue` preserves JS property order and any lone surrogates
  in metadata. Use it with `jsjson.Stringify` at the JS-compatible wire boundary.
- Scope precedes expiry and validation. Validation runs before file reads and
  again before adoption. Ranges are byte-based, nonnegative JS safe integers.
  Offset and length remain float64 until validated; fractional, nonfinite, and
  unsafe values are not rounded or silently accepted.
- Expiry is fixed at publication, never sliding. Default retention is 30 minutes.
  The sweep interval matches `min(ttlMs, 60000)`, with Node's small-delay coercion.
  Expiry, recalls, plugin disable, and shutdown revoke access. Removal is immediate
  even when unlink fails, and shutdown retries recursive directory cleanup.
- Pending writes are revocable before publication. Invalidation takes precedence
  over cancellation inside the write transaction. Closed-cache, invalid-range,
  expired, not-found, invalidated, cancelled, and storage errors remain distinct.
  `Read` returns the caller's cancellation cause, like source `throwIfAborted`;
  `Store` normalizes aborted writes to `tool_result_cancelled`, preserving an
  existing `tool_result_` error according to the source's catch ordering.
- A new cache starts empty in a new 0700 directory, with 0600 result files. There
  is no restart discovery, persisted result index, generation map, or replay.

## Go ownership and cancellation contract

`New(Config{})` starts the expiry goroutine. Always call `Dispose` when the owning
runtime shuts down. A cache is safe for concurrent Store, Read, invalidation, and
Dispose. No user callback or blocking file I/O runs under the cache mutex.

Each source is single-use. `Next(ctx)` returns a byte chunk or `io.EOF`; empty
chunks do not end a stream. A source with a final data chunk must return that chunk
before a separate EOF. A source may reuse its chunk buffer on the next `Next`,
but must not mutate the current chunk before that next call. The cache uses one
request/result pump per Store, with at most one Next in flight and no prefetch,
not a new goroutine for every chunk. It introduces no stream-length limit.

Cancellation races the in-flight Next, so neither Store nor Dispose waits for an
arbitrarily stalled iterator. A detached Next retains only its source and context,
not cache state or file handles; its late bytes/error cannot publish a result.
Go cannot forcibly terminate arbitrary user code. Sources must eventually honor
context cancellation or release their blocked call through `Destroy` to avoid
retaining that goroutine. This is a callback-lifetime requirement, not a timeout.

Optional `Destroy` is immediate, nonblocking, idempotent, and safe to call
concurrently with Next. Incomplete failed iteration can invoke it twice, matching
the source's write/store cleanup. Optional `Return` is dispatched asynchronously,
may overlap Next, and is never awaited. Destroy/Return panics and Return errors
are contained. Return and a stalled Next may outlive Store, so captured resources
must remain valid until those calls actually exit.

Owner fields and slices are snapshotted when Store starts; guard closures remain
live. This is the explicit immutable Go boundary used for concurrency and ownership
indexes. It matches the actual runtime capture in `src/plugins/manager.ts:81`:
`resultOwner` makes fresh dependencies and validation arrays, clones source
snapshots, and passes the fresh object directly into cache.store at lines 492/515.
Arbitrary external mutation of that TS owner object while writing is not a Go
integration contract. Adoption receives a separate slice snapshot rather than a
mutable reference to the cache's private entry.

Guards, clocks, and adoption callbacks must be concurrency-safe and promptly
return. Guards return `(false, nil)` for invalidation or an error for a thrown
source guard error. Adoption returns an error for a thrown source adoption error.
These callbacks may reenter reads/invalidation. Do not synchronously Dispose the
cache from its own Store/guard/clock callback: shutdown waits for that operation
or the sweep to finish. Request shutdown from the runtime owner instead.

A successful read linearizes at its final membership check. Invalidation can run
after that check; adoption itself may revoke the entry while still returning the
already-read view, just as in the source. Scope checks do not reveal another
owner's expiry or guards.

Plugin and `(scene, dependency)` indexes select only matching active entries and
writes. Recall/plugin disable do not scan unrelated results. Index membership is
removed with the entry/write, including errors, zero-byte preview failures,
expiry, and shutdown. Duplicate dependencies do not duplicate index work. The
expiry sweep retains the source cadence and is not a new admission policy.

## Differential and boundary verification

From the repository root, using the pinned Node 24.22.1 and Go toolchains:

```sh
node --import tsx backend/tests/generate-tool-result-fixtures.mjs --check
cd backend
go test -race ./internal/toolresults
go vet ./internal/toolresults
```

Omit `--check` only when intentionally regenerating reviewed synthetic fixtures.
The generator imports the actual TypeScript cache, writes real private temporary
files, and pins source SHA-256
`6a74dee6988aca28f22adb1f392dd8d7cd75e1dea23922fc7a8b280e1375a08b`.
It does not copy the cache algorithm to calculate expected results. UUIDs are
normalized; complete result JSON, content hashes, decoded-byte hashes, nested JSON
byte counts, errors, adoption, files, entries, and pending-write counts are compared.
UTF-16 input arrays and lossless metadata parsing prevent the fixture transport
from destroying surrogate cases.

The oracle covers text/binary/stream previews, nested escaping, byte boundaries,
base64 fallback, exact range errors and precedence, TTL, scope isolation,
recall/plugin invalidation, pre/post-read validation, adoption/reentrancy,
missing/truncated files, source errors, shutdown/restart, and cancellation of
stalled Next with an independently stalled Return. Native tests also reconstruct
a multi-megabyte stream from real files, verify private permissions and failed
unlink cleanup, exercise concurrent ownership, contain hostile cleanup hooks,
and count targeted invalidation work among 10,000 unrelated owners.

This is a cache-domain checkpoint, not a claim that the entire plugin capability
bridge or robot runtime has reached production parity.
