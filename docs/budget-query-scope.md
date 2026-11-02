# Budget ledger query scope

The admission ledger no longer materializes every historical attempt for coverage,
contract validation, or turn limits. Coverage uses one SQLite statement containing
four indexed `UNION ALL` arms: the exact current UTC window, all nonzero holds,
all overrun flags, and all unbounded-exposure flags. Results are deduplicated by
attempt ID before accounting. Historical flags and unresolved/active holds remain
lifetime-global; old settled attempts are not silently treated as current costs.

The nonzero predicates intentionally use `IS NOT 0`, not positive-only predicates:
malformed negative/fractional/blob holds still reach the existing BigInt validation.
All monetary addition remains in JavaScript BigInt followed by safe-number checks;
there is no SQLite monetary SUM or imprecise aggregate conversion.

Contract-overrun validation reads the global partial overrun index. Turn limits use
the existing turn index with no date restriction; released attempts remain excluded.
A nonunique expression index accelerates logical request/attempt duplicate checks
without changing their existing explicit error or retry behavior.

New indexes are created transactionally at startup. A malformed legacy JSON payload
fails index creation closed, earlier than the old lazy admission parse. The migration
rolls back all newly created indexes and leaves the original row unchanged; repair of
such corrupt data is not performed automatically.

Validation: `tests/budget-query-scope.test.ts` uses 20,000 historical settled rows,
checks returned row counts and query plans, UTC rollover, active holds, lifetime
turn limits, retries, historical flags, exact safe-integer limits, malformed holds,
and migration rollback. With four relevant attempts coverage returns five arm rows
(one overlapping current hold), not the 20,004 historical/current table rows.

This bounds reads by relevant exposure, not by a fixed row cap: many active holds,
current-window attempts, or historical contract failures still require work. It does
not truncate accounting, expire active reservations, or perform financial/model calls.
