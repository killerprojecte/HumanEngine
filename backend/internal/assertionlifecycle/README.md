# Proof invalidation, not complete recall

This package ports the synchronous `ProofInvalidation` lifecycle onto real
assertion repositories, retained history, revision coordination and durable
assertion authority. It accepts an existing `RevisionTxn`; it does not open an
independent authority or silently rebind a suspended ancestor transaction.

- History-first/current-row reverse-link DFS preserves source insertion order.
  Current links include inactive records and invalidated proofs. Historical-only
  children are captured before deleting their retained reverse links.
- The current reverse graph is built once per purge/refresh operation, never
  retained across mutations. Traversal is iterative with no new resource caps.
- Independent OR alternatives survive; only selected dependent proofs receive
  `invalidatedBy`, retaining their version bindings and unknown metadata.
  Dependency lookup preserves lazy short-circuit order even when an untouched
  later parent is malformed. An actually visited non-string dependency remains
  a typed-boundary error; raw corrupt-value SQLite binding parity is not claimed.
- Refresh analyzes every verified active/disputed record before emitting any
  privacy intent, then constructs the complete current graph. The authority's
  batch API shares only private positive provenance results, with a fresh source
  budget and empty path for each root. No callback can interleave a mutation or
  checkpoint into that batch. Unknown/budget failure is not unsupported evidence.
- Purge removes actual retained bodies/indexes and increments deletion epochs.
  Surviving rows are touched so the caller's coordinator rebuilds safe history
  using its required filtered HistorySupports callback. Unsupported current rows
  are written through the real repository, with eligibility intent production,
  and actual `memory_vectors` rows are deleted.
- Ordinary nested errors keep the source savepoint behavior. Durable intents
  survive main rollback; native rollback/cancellation expires the root. The
  resulting main ACK gap continues to block publication.

`Owners` deliberately requires projection cleanup, pre-history-purge cleanup,
AND pre-record-invalidation cleanup. The callbacks receive the current bound
Store. Its trusted `WithWriteSession` bridge validates ownership/active scope,
releases the session mutex before owner calls and never acquires another
connection. No plugin or model-facing read facade exposes this write bridge.

The relation/projection/working-set and knowledge/semantic owners are **not
implemented by this package**. There are no production no-op defaults. In
particular, native FTS5 is not currently enabled in the default driver build;
these tests do not replace FTS5 with a plain table or claim projection parity.
The oracle uses explicit recording derived-owner ports while exercising actual
repository/history/authority/coordinator/vector SQL. Full recall/delete, source
root ordering, commitments, identity metadata and runtime composition remain
separate work. There is no production startup wiring in this slice.

Generate/check the source oracle with pinned Node v24.22.1:
`TZ=UTC node --import tsx backend/tests/generate-proof-invalidation-fixtures.mjs`
(add `--check` to check source digests and output). It uses independent on-disk
SQLite main and authority databases and records exact event order, SQL hashes,
durable intents and main gate reader requirements. Run Go tests for
`./internal/assertionlifecycle ./internal/assertionauthority ./internal/assertions`
and the existing source tests `memory-proof-lifecycle` and
`memory-history-lifecycle`. Native integration additionally covers SQL failures,
malformed analysis, lost transactions, same-session owner binding and expiration.
