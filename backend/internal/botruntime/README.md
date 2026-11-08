# Native scene and turn orchestration

This package is an implementation checkpoint, not a production-backend replacement.
It runs the native conversation lifecycle with injected native services. It never
calls the TypeScript runtime. The production executable still uses its existing
read-only preview composition; this package does not silently enable real sends.

## Ownership and flow

- `scene.go` admits scenes and simulation sessions. Simulation admission reserves
  the session before appending input, so competing requests cannot both mutate it.
- `ingress.go` implements normalized ingress, ordered edit replacement, recall
  callbacks and cancellation. Bot ingress bypasses source enrollment, matching the
  source. Real authority is mandatory; a synthetic authority exists only in tests.
- `admission.go`, `timers.go`, and `topic_ownership.go` compose topic routing,
  automatic eligibility, FIFO admission, explicit WAIT rechecks, reassignment and
  protected-send ownership. WAIT without fresh input does not call a model again.
- `prepare.go`, `participation.go`, `planning.go`, and `replyer.go` execute the full
  participation decision, native planning/read/repair loop, sequential guarded
  actions, evidence/brief packing, Replyer tool loop and grounding fallback.
  `turn.go` retains stage lifetimes across the whole flow.
- `send.go` composes hook transforms, plan/grounding checks, exact-word stale
  revalidation and `dispatch` sequential transport. Receipts include partial,
  failed, cancelled and unknown outcomes. Unknown sends are not retried.
- `turn_cleanup.go` and `lifecycle.go` release only their own scene/turn generation,
  while retaining unknown-send protection independently of message bodies.
- `messages.go`, `view.go`, and `retention.go` keep immutable query publication and
  body retention separate from durable authority, topic identity and send receipts.

## Required service boundaries

`Services.Authority` is required in **both** real and simulation modes. Its reads
must enforce recovery readiness, scoped recall/source revisions and the exact raw
configuration authority (settings/models/rules/scene rows and current tool set).
Equal parsed settings do not imply equal raw authority. Simulation may read these
control-plane facts, but never enroll or persist messages, query real adapter
history or transmit through a real adapter.

The durable Store/source-event implementation remains a separate migration task:
SQLite transactions, root intent and quarantine on preparation failure, commit
callbacks, ordered revision CAS, enrollment and identity storage cannot be
replaced by the permissive test fixture. The runtime passes explicit preparation
and apply callbacks to that service and preserves failure ordering.

`Models` and the optional `PolicyModels` compose the existing native model policy
with a required provider invocation. Actual provider credentials, budgets,
accounting and continuation lifetimes belong to that native service. Transport,
plugin execution, media/image preparation, display authority and source reads are
explicit interfaces. There is no fallback success authorization or fake provider.
Full production event-bus/SSE delivery, flow-log persistence, startup recovery and
HTTP write-route composition are not claimed by the diagnostic `Trace` sink.

Values use ordered `jsjson.Object`/JSON types and WTF-8 strings. The production V1
model source already normalizes visible output through JSON stringify/parse before
tool validation. Fixtures record that boundary separately from raw Zod behavior:
unknown properties containing `undefined` fail raw strict validation, but are
removed by that existing wire normalization. `null` is never treated as missing.

## Query and hot-path costs

Snapshot and exact PreviewMessage use an atomic immutable view, then briefly
join a per-scene publication mutex to commit irreversible cache retention. They
never acquire the scene mutex or hold that publication mutex across SQL, model,
tool, or transport work. This is not a lock-free or universally wait-free path:
a reader may wait for another reader or an owner publication. Logical trim does
O(n) record checks and allocates O(n) temporary references, without cloning
unrelated message bodies. Exact preview clones/projects only its selected body;
Snapshot copies its retained result. Display projection occurs after publication
unlock and can still wait on its own authority service. Scoped preview accepts
the caller's active ReadAuthority rather than opening a nested transaction.

Reader eviction marks only in-memory membership tokens, never durable source
revocation. Owner Lock/TryLock reconciles these removals before mutable indexes
are used; publication reconciles again for readers concurrent with an existing
owner operation. Same-membership body replacement shares a token; genuine edit
re-admission receives a fresh one. Increasing cache lifetime or re-enabling the
cache cannot revive previously discarded memberships.

Immutable message records parse receivedAt exactly once using `jsdate.ParseMillis`.
ID/body/availability indexes handle duplicate lookup and replacement. Publication
shares persistent 32-reference blocks; 32 is a structural allocation unit, not an
admission limit. Unexpired, under-policy ingestion skips retention scans. Tests
show 201 ingests make 201 parses and zero body-trim visits; query does not reparse.
The source's existing 500 retained-body policy and existing history/ACK/operation
policies are unchanged. Capacity pressure or expiration still scans the retained
set. Topic publication still counts topic message IDs, so very long protected
histories retain that source-sized scan; incremental topic counts are a separate
optimization, not disguised by a new cap.

Operation receipts have per-turn ordered references and per-scene keys. Outcome
presence is O(1), retrieval is O(the current turn's operations), and inherited
oldest-ledger eviction is O(1). Tests insert unrelated sentinel turns that would
fail if lookup touched them. Body eviction never erases unknown-send protection.

## Evidence

`backend/tests/generate-bot-runtime-fixtures.mjs` executes the actual TypeScript
Runtime against synthetic in-memory stores, models, extensions and transports.
It produces 31 turn scenarios with complete deterministic model requests (persona,
system instructions, user evidence and tool schemas), ordered model/tool/send/
receipt effects and final outcomes. Only generated topic UUIDs are mapped to a
stable ID; business fields and request contents are not discarded. Six ingress
scenarios cover bot identity normalization and enrollment. Recall failure cases
record exact preparation/callback order and post-failure state. Tool schemas have
wide boundary fixtures, including astral text, strict objects, defaults and unions.
Source fingerprints fail when source changes or is missing.

Native tests additionally exercise blocked-model queries and concurrent ingress,
recall/edit, A→B→A revision revocation, wait generations, asynchronous transmit
guards, unknown cancellation, cross-scene rejection, simulation isolation,
concurrent simulation admission, extension removal and replacement-scene cleanup.
Grounding has separate actual-source fixtures and generated Unicode classifications.

Run from the repository root with pinned Node v24.22.1:

```
node --import tsx backend/tests/generate-bot-runtime-fixtures.mjs --check
cd backend
go test -race ./internal/botruntime
go vet ./internal/botruntime
```

Named-zone clock/date coverage requires the repository's pinned native ICU build
and `-tags humanengine_icu`. The runtime defaults its h23 hour to `intltime.Hour`
and applies Date TimeClip before crossing that API. It does not start Node to
infer deployment locale or timezone.
