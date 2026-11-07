# Assertion storage candidate

This package owns native current-record/proof persistence, history, revision
batching and live eligibility intents. It is not a MemoryService implementation
or an authorization facade. Reading a stored history revision never establishes
permission to show it to a model or API caller.

`New` is non-mutating. `Initialize` explicitly creates/migrates the fixed storage
schemas inside an owned main write transaction. `Read` borrows a verified
publication scope; readers and transaction-bound stores expire with their owner.
`WithSession` joins a trusted existing write capability. `Transaction` creates
an owned nested savepoint when already bound, and passes its scoped child to the
callback. Controlled writers enable the source Store's foreign-key setting on
each borrowed connection before BEGIN; fixtures also use a foreign-key-enabled
DSN. Uncontrolled direct database users must configure their own connections.
Ancestor handles are suspended during that callback, not implicitly
rebound through global state.

`Coordinator.Transaction` exposes a scoped `RevisionTxn`. Use its `Repository`
and pass the nested child to nested work. Caught nested errors restore SQL and
revision bookkeeping. Checkpoints commit final record/proof pairs to history;
permission changes accumulate until the outer coordinator boundary. The explicit
projection, retention and authorized history-support callbacks must be supplied
by the owner, with the active bound Store passed to each. The coordinator never silently substitutes raw proofs for the
facade's filtered historical evidence. They are not
replaced with production no-ops. AfterCommit follows the source coordinator
boundary, which can itself be inside another caller-owned SQL transaction.

Durable assertion and eligibility intents are independent of main rollback.
Coordinator flush reads only the latest valid retained revision, skipping malformed
trailing rows rather than decoding all retained bodies on each write.
A main ACK gap remains a recovery obligation, not a reason to retry a mutation
blindly. The source's existing history query widths and idempotency-key rule are
preserved; this package adds no admission or resource cap. History purge erases
bodies and reverse evidence indexes while retaining monotonic version/epoch
state. Terminal erase prohibits reuse of that assertion ID.

Fingerprint text uses the pinned native `jsstring` implementation, including
Unicode 17 case data, final sigma and lone UTF-16 surrogates. Hash material uses
lossless JSON and source-defined object/array ordering. The coordinator's date
parser is an explicit source timezone dependency; UTC is the standalone default,
not automatic deployment timezone discovery.

Remaining work includes the source's scoped prepared support-read optimization,
provenance invalidation, projection/commitment/knowledge/semantic privacy stores,
complete model-facing authority, offline reconstruction, and same-session runtime
recall composition. The current runtime read-only recall extension capability
cannot perform these missing writes. No API or production startup is enabled by
this package, and its tests are not full memory recall or performance acceptance.
