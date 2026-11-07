# Topic-local turn admission

This is a complete port of `src/runtime/turn-wait.ts`, not a bot server. It runs
actual admission transitions in Go without forwarding work to TypeScript. This package does not change the
production runtime, REST API or Nuxt frontend.

The oracle is master `24fe8a773d87f2f09996e259fca46524c2c02928`, with SHA-256
`bf01e8b12103b76a0954546f7882137a531d9efff4961bf38b0cb627df3e1217` for the source
file. The generator rejects a different source or Node version rather than
silently blessing changed behavior.

## Ownership and integration contract

- One coordinator belongs to one topic. Its owner serializes method calls and
  supplies the clock. There are no goroutines, timers, model calls, adapter calls,
  storage operations or new policy limits in this package.
- `Observe` copies an event, retaining insertion order. Duplicate detection
  precedes capacity checking. Rejected full events are not marked as seen.
- `NextWakeAt` returns the earlier pending deadline or existing WAIT deadline,
  unless an attempt remains active. Pending deadlines are the minimum of
  last-arrival plus quiet time and first-arrival plus maximum wait. Addressed
  events can shorten quiet time and are meaningful even without the relevance bit.
- `TakeReady` returns run, skip or one cheap recheck. Only run owns an active
  attempt. A WAIT expiry cannot pull an unready fresh batch forward and cannot
  grant another model call by itself.
- Keep the exact returned `*Wake`. A copied or foreign wake cannot finish the
  attempt. New ingress, relevant recall and trusted replacement invalidate its
  generation without releasing ownership. Only `Finish` or `Cancel` releases it.
- `Finish` never requeues consumed input. It accepts a future finite WAIT only
  for the current generation, at most once for that generation, clamped by the
  existing `MaxModelWaitMS` policy. Past, zero, nonfinite and stale waits are terminal.
- `Recall` removes pending input and a WAIT using that input, but keeps replay
  deduplication. `Replace` forgets the old ID and lets the owner observe an explicit
  trusted edit. It does not authorize transport retries.
- `Configure` changes future calculations without resetting input, wake identity,
  generation or deduplication. It does not resize existing pending/seen state.
- `Cancel` clears pending, active and waiting state but keeps seen IDs. A new
  coordinator starts empty and does not reconstruct queued work from storage.
- IDs are opaque strings; no normalization, truncation or numeric conversion is
  performed. `RetainedEventIDs` returns ordered independent storage and excludes
  the separately bounded replay-deduplication set.

Current runtime mapping remains outside this package:

| TypeScript source                       | Existing responsibility                                                                                                                                            |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `src/runtime.ts:1035-1100`, `1219-1282` | Configures quiet=`min(debounceMs,maxWaitMs)`, addressed=`min(debounceMs,500)`, maximum model WAIT=60,000 ms, pending=60; schedules timers and starts actual turns. |
| `src/runtime.ts:1203-1216`, `1235-1257` | Applies direct/group cooldowns, scene concurrency and simulation exceptions before consuming a wake.                                                               |
| `src/runtime.ts:854-893`, `1285-1344`   | Qualifies explicit edits and durable recalls, invalidates source dependencies and calls replace/recall.                                                            |
| `src/runtime.ts:1349-1367`, `3094-3153` | Cancels scenes, finishes the exact wake, handles recovery failure and schedules only buffered fresh ingress, oldest topic first.                                   |
| `src/runtime.ts:1718-1740`, `2110-2167` | Checks live source/recovery eligibility, invokes participation Planner and maps a WAIT to an absolute timestamp.                                                   |

Do not move these responsibilities into `TakeReady` or treat this package as
durable replay prevention. Runtime ingress owns history rejection, scene matching,
tombstones and registered-source replay detection (`src/runtime.ts:896-979`).
The separate `WorkScheduler` in `src/scheduler.ts` owns global model lanes,
foreground reservation, learning fairness and model request rate limits.

## Executable differential contract

From the repository root:

```sh
node --import tsx backend/tests/generate-turnwait-fixtures.mjs --check
cd backend
go test -race ./internal/turnwait
go vet ./internal/turnwait
```

Omit `--check` only to regenerate the synthetic fixtures after reviewing source
and test changes. The generator imports the actual TypeScript coordinator and
uses a fake numeric clock. It never reimplements expected transitions.

Each trace starts with configuration and then runs timestamped operations:
observe, recall, replace, takeReady, finish, configure, cancel and restart, plus
test-only identity/copy probes. A saved wake handle refers to the original returned
object, never a JSON-reconstructed object. Every operation records its return
value or exact error, optional next wake time, optional pending-addressed state,
retention flags, ordered live event IDs and currentness of every saved wake.
Absent return values remain distinct from false and zero.

Numbers transport NaN and infinities with explicit tags for invalid configuration
and WAIT tests. IDs transport exact UTF-16 code units, including lone surrogates,
so the test harness cannot accidentally merge them through Go's standard JSON
decoder. The Go API receives opaque, losslessly encoded IDs in these fixtures.
This is not a claim that the unfinished production JSON decoder now preserves
lone-surrogate strings.

The fixture covers quiet/max-wait boundaries, addressed/no-signal decisions,
WAIT/fresh-input ordering, stale and duplicate finishes, recalls, edits, config
copying/shrinking, queue fullness, seen-set FIFO eviction, cancellation/restart,
cross-language number boundaries, opaque IDs and fixed-seed stateful sequences.
Additional native Go tests cover separate coordinator ownership and the exact
clock-read boundaries.

## Still outside this checkpoint

Planner tools/evidence/repair, action execution, Replyer expression, guarded
sequential send receipts, adapter cancellation arbitration, durable source and
recall authority, global model scheduling and production bot composition are not
implemented by this package. Their existing behavior must be tested separately
before replacing the runtime. No full-bot or production-readiness claim follows
from these admission tests.
