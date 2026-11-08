# Commitment command admission checkpoint

`AdmitInAssertionScope` composes normal propose/amend/confirm/complete/cancel
admission with exact assertion current authority, the active assertion child,
typed recovery intents and real working-set mutation/resolution owners.
`complete` is the source command; `done` is its lifecycle result.

The input must be the original trusted MemoryContext with commitment
`exposedRefs`; backing is the exact checkpointed assertion ref. This function
must be called inside the owner's assertion transaction. Let uncaught errors
escape that child: it does not turn errors into semantic rejections or create
an independent transaction. Durable intents may outlive a rolled-back main
write, deliberately leaving a recovery ACK gap.

Normal transitions persist projected states for active/latest terms versions.
They share PrivacyService's typed intent mechanism only, not its null-state
privacy writer. Revisions retain immutable terms and recordedAt. Source people
semantics reread the backing authority, retain source senders without trusted
evidence, and include nonbot mentions from the first available nonbot matching
trusted evidence. No latest-version rebinding or new resource cap is added.

The only injected production runtime behaviors are current invocation enable
settings, deterministic ID override and optional workspace dirty scheduling.
Assertion current reads, scene learning policy, beforeWrite journaling and
workspace mutation/resolution are concrete. Dirty notification is optional to
match the source listener; runtime composition should pass its scheduler.

## Validation

- Actual TS `CommitmentService.admit`/normal writer oracle: 16 operations,
  deterministic IDs/time, runtime pinned to Node v24.22.1 and source SHA-256s.
  Also includes two caught SQL ABORT phases, 8/9-participant and 64-evidence,
  revision and proposal-group boundaries, plus actual source onPlanCommitted
  dependency batching of 41 references as 40 + 1.
- Consumer compares command results, complete snapshot/timeline hashes,
  afterWrite prior/final revisions and typed durable head qualification/ACK.
- Actual memoryservice owners test two participants, activation, completion,
  amendment, cancellation, stale exposure, idempotency, nonnull temporal states,
  real historical temporal readback, 41-dependency working-set jobs and late
  SQL failure rolling back all main writes while
  retaining the durable intent and preventing publication across its ACK gap.
- Actual assertion authority tests sender fallback, expired handle, suspended
  ancestor rebinding, first trusted match/bot filtering and cancellation after
  owner callbacks. Native transaction-loss injection is a preinstalled command
  DML RAISE(ROLLBACK) trigger, with subsequent command/SQL ErrTxDone assertions,
  unchanged main state, retained durable intent and no new ACK/autocommit.
  Ordinary caught ABORT cases instead preserve the source persisted prefix
  and allow the still-live owner to continue and commit its exact ACK.

These Go tests were authored and gofmt-checked in a private additive overlay;
not compiled/run by this worker under the shared-machine resource restriction.
The parent must run tagged focused tests/race/vet after integration.

## Boundaries

This is not outer ProposalCommand, declaration preparation, robot wiring, REST
parity or full MemoryService migration. Public interactive erase/history are
unchanged. Existing package overview text predates this additive checkpoint.
Source-oracle current/afterWrite ports intentionally isolate command behavior;
real-owner behavior is covered separately. The oracle uses recording people
ports; concrete sender/mention matching lives in the native command authority.
Schema error wording is not asserted byte-for-byte against Zod.
