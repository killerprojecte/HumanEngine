# Native source-learning coordinator

This owner keeps source payloads only in ephemeral queue, active, survivor,
retry and failure state. It composes the existing metadata admission policy,
retains the source timing, capacity, batch and retry limits, and never scans
stored messages at startup. Trusted runtime edit/recall/scene hooks invalidate
old revisions. Scene disable deletes recall survivors even if recall supplied
the first cancellation reason.

`Progress.Applying` is the permanent no-replay barrier. `WritePending` and saved,
rejected and ignored totals describe the outcome independently. A zero saved
count does not prove that a failed write did not commit.

`CloseAndWait` seals admission, cancels active work and joins physical callbacks,
policy runners and metadata SQL observers before returning. Draining one batch
never disposes the borrowed shared model owner. The physical group is waited
outside accepted model callbacks, avoiding self-wait.

The actual TypeScript coordinator and batch-runner produce frozen differential
traces for timers, fairness, retry/discard, recalls, edits, off/on and partial
writes. Native tests separately cover cancellation-ignoring callbacks and the
real model-call SQL observer.
