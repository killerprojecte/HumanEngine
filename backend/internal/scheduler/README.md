# Global model work scheduling

This package executes the work scheduler in `src/scheduler.ts` in Go. It does not
forward operations to TypeScript. It is not wired into production bot startup,
and does not implement provider protocols, model diagnostics, runtime learning
batching, Planner/Replyer, or transport delivery.

The frozen oracle is master `24fe8a773d87f2f09996e259fca46524c2c02928`.
The scheduler source SHA-256 is
`3340a98e5545c146a2332dc335931efeb8c577806d08cb2d05f1c92d30b52d36`.
Fixture generation checks the source hash and Node 24.22.1 before importing the
actual TypeScript implementation.

## Executable integration contract

- Share `ModelScheduler` (or one injected `NewModelScheduler`) across all model
  callers. The model defaults are concurrency 4, maximum queued 256, one reserved
  foreground slot, and maintenance aging after 300,000 ms. `NewDefault` retains
  the source's unreserved generic defaults. These are existing product policies,
  not added protective limits. Neither constructor starts background work.
- `Submit` takes explicit priority and learning options, like `WorkScheduler.run`.
  `SubmitModel` additionally applies `EffectiveModelPriority` and the inherited
  learning deadline, like the policy part of `scheduleModel`. Diagnostics remain
  outside this package. There is no implicit learning privilege for priority 1/2.
- A `Task` is an asynchronous starter: begin the operation and return promptly,
  then call `complete(value, err)` when the operation actually ends. Start blocking
  Go work in a goroutine inside that starter. Starters execute in admission order
  outside the scheduler lock, can submit nested work, and must not block waiting
  for a job in this scheduler. This is the equivalent of returning a pending
  JavaScript promise, not a requirement that task work finish synchronously.
- `Future.Wait` returns the result. Duplicate completions cannot free another
  slot; callback errors preserve identity and non-error panics become
  `work_failed`. Completion is deferred past the starter's return.
- The caller's context values, model lane, deadline and cancellation propagate
  into the starter. Context helpers return child scopes and never mutate their
  parent. Nested background scope cannot elevate urgency; nested learning scope
  keeps the later deadline. Completion does not cancel a task's captured context:
  nested work may still depend on it. Callers retain their normal responsibility
  to cancel request contexts they own, including after the operation completes.
- Parent context cancellation removes queued work asynchronously through Go's
  context callback. `Future.Cancel` is synchronous for queue removal and affects
  only this request and descendants, not siblings or its submitting context.
  Once admitted, cancellation is observable by the task but does not skip its
  invocation, synthesize a result, refund a rate start, or free its running slot.
  Only the task's actual completion releases running ownership.
- Configuration validates the source's concurrency 1–32 and call rate 0–6000;
  failure is atomic. The constructor only validates safe-integer reservation and
  aging, matching the source rather than imposing additional constructor rules.
  Changing concurrency never preempts work. Disabling rate limiting clears the
  tracked starts; enabling it does not charge earlier unlimited starts.
- FIFO is the tie breaker. Only original priority 2 ages to 1; foreground remains
  ahead. Explicit overdue learning gets the oldest eligible turn after four
  foreground starts while overdue learning waits, subject to both the original
  nonforeground capacity and the same rolling-minute rate allowance. Cancelling
  all overdue work clears that allowance; cancelling only one preserves it.
- Rate is charged at admission, with starts strictly newer than `now - 60,000`.
  No aging/learning timer is added. A rate timer exists only when capacity admits
  a queued candidate but rate does not. Clearing/replacing timers preserves wake
  ownership even if a Go callback has already fired concurrently.
- A scheduler has no restart persistence. Constructing another scheduler begins
  empty. Runtime shutdown must cancel caller-owned contexts and await actual
  running operations; this package does not invent forced termination or replay.

## Differential and concurrency checks

From the repository root:

```sh
node --import tsx backend/tests/generate-scheduler-fixtures.mjs --check
cd backend
go test -race ./internal/scheduler
go vet ./internal/scheduler
```

Omit `--check` to regenerate after review. The generator imports the actual TS
scheduler, injects a numeric clock and fake timers, and drives held/automatic
promises and AbortControllers. The Go test drives the production scheduler with
the same operations and fake clock, replacing only callback launch timing.
There is no copied expected-policy implementation in either harness.

The 41 traces cover 3,431 transitions, with 12 invalid scheduler-option cases and
64 context combinations. This includes the existing model-priority, call-rate
and learning-fairness scenarios, plus 12 fixed-seed stateful interleavings.
Every operation compares status, new start events with captured scopes and
cancellation, changed promise outcomes, and all pending rate-wake deadlines.
Delta events/outcomes keep fixtures small without discarding transitions.
Nonfinite numeric inputs use explicit tags, not lossy JSON nulls.

Native tests also exercise simultaneous submissions/completions/cancellation,
actual goroutine dispatch, parent cancellation, nested scheduling, duplicate
completion, error/panic identity, admitted-before-cancel invocation, stale timer
ownership, option capture, and default policy constants. Passing these checks
establishes this scheduler slice, not end-to-end bot or migration readiness.
