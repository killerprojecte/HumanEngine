# Ingress routing and identity

`Router` owns two source-compatible effects before message admission: discover a
missing persisted scene with its feature switches disabled, then observe its
OneBot presentation identity. `Repository` makes those effects explicit;
`SQLiteRepository` borrows the existing main pool and shared authority coordinator.
It never starts Node, fetches a remote profile or rewrites routing identifiers.

Discovery and profile updates retain the source's separate transaction boundaries.
A newly discovered scene can be committed even when a later identity read fails.
That does not mean the message was admitted. The runtime retains transport
registration on such a failure without creating a message-state owner.

Identity observation precedes closed/duplicate/recall decisions and also runs for
bot messages. Simulation, history and mismatched-scene rejection belong to their
original runtime boundaries. Accepted edits re-enter this routing path after
revision observation and edited callbacks, without observing the revision again.

Presentation rules live in `profileidentity`: exact UTF-16 name normalization,
the source's existing 128-code-unit display rule, canonical QQ avatars and encoded
OneBot identity matching. Raw persisted identity values keep JavaScript fallback,
coercion and error behavior; unknown old properties are removed only when the
source would persist a different complete identity shape. Identical JSON does not
produce a write. These are not new admission limits.

Scene updates reuse `scenestore` revision/revocation policy. Ordinary identity
config writes preserve the previous-row parse inside their transaction but do not
advance unrelated security configuration. Reads and writes use the shared recovery
coordinator; there is no global active-transaction bypass or independent authority
connection for each message.

The actual TypeScript oracle is `tests/generate-ingress-routing-fixtures.mjs`.
Tests separate routing SQL behavior, runtime callback/admission boundaries and
combined runtime/dual-database effects. These checks do not establish full backend
parity, production REST composition or a universal performance bound. Large JSON
rows still require parsing, and independent authority writers can require another
history audit under the existing recovery contract.
