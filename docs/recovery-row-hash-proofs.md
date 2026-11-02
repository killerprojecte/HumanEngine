# Bounded pure recovery-row SHA proofs

## Why no persistent authority-token cache

SQLite's `data_version` changes for commits by other connections, not for same-connection commits, and comparisons are meaningful only on the same connection. `total_changes()` counts DML modifications, not arbitrary operations. Neither is a cryptographic commitment to database bytes. See the official [data_version contract](https://sqlite.org/pragma.html#pragma_data_version) and [total_changes contract](https://sqlite.org/c3ref/total_changes.html).

A synthetic counterexample is retained in `tests/recovery-row-hash-proof.test.ts`: validate an authority, overwrite a same-length fingerprint payload directly in its temporary file, then release SQLite's page cache. The connection's data version, total changes and schema version remain unchanged, but the original full chain read detects `recovery_chain_invalid`. Consequently caching a validated head by these tokens would weaken detectable corruption behavior. That design was rejected, not implemented. The direct-file write is fault injection against disposable test data, not a supported database operation; SQLite itself warns about [external overwrites and live restore hazards](https://sqlite.org/howtocorrupt.html).

## Accepted scope: pure SHA comparison only

Every `entries()` invocation still reads the current identity and **every** chain row. It still checks identity count/format, sequence order, previous-hash continuity, parses the current payload and validates its event schema. `head()` still reads the fresh head, and the existing recovery gate compares current authority metadata. No validity lease survives a call.

The only reused fact is a successful pure SHA comparison for an exact tuple:

`[instanceId, sequence, previous_hash, payload, hash]`

Keys are unambiguous JSON encodings of the original primitive string fields and safe integer sequence. Changed bytes, identity or sequence cannot match another tuple. No parsed revocation, recovery head, authorization decision, failure or partial validation is cached. The event parser executes even on a SHA hit; `entries()` always returns newly parsed objects, so callers cannot poison future replay by mutating a returned event.

The cache is private to each authority instance and cleared on close. It admits a bounded prefix of successful proofs: at most 4,096 entries, an 8 MiB **logical accounting** budget (two bytes per key character plus 256 bytes per entry), and 64 KiB per key. This is conservative accounting, not a claim about exact JavaScript heap/RSS. A raw-field length preflight bounds temporary key construction; oversized rows are fully validated without admission or truncation. Once full, new tuples remain uncached rather than evicting a useful stable prefix. No TTL or mutation counter is used as evidence of validity.

## Validation and limits

Focused regressions cover every tuple field, identity change, deletion/reordering, rollback, reopen, independent returned objects, valid-SHA invalid event schemas, unchanged-token raw-file tampering, entry and byte caps, and oversized valid JSON payloads. Existing read-only source/dependency scopes remain independently tested.

This preserves full-chain SQL traversal and therefore remains O(chain length). It targets repeated SHA allocation/work; it does not eliminate SQLite reads, JSON parsing, synchronous event-loop work, or address arbitrary plugin execution. Benchmarking the exact integrated receive-only workload is required before claiming a performance improvement.
