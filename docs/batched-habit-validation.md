# Bounded recovery verification during habit reads

Habit identities remain live qualifications, not cached authorization. Each identity batch rereads its requested raw records, current source evidence, scope, policy and time-derived eligibility. The engine captures identities before exposing hints and registers one batch validation for those exact hints; it rechecks them at each existing validation boundary.

`currentDomainSources` runs only the fixed `domainSourceCurrent` read over copied inputs. It has no callback parameter. Under the existing supported main-WAL/authority-DELETE configuration, it uses synchronous read-only snapshots of both databases, verifies the complete authority chain before the reads, enforces transaction/change/schema invariants throughout, and performs a fresh complete verification after releasing the snapshots. Existing transactions or unsupported configurations retain the original per-read checks. Errors restore both connections' writable state. Returned booleans are source qualification results for that call, not reusable authorization tokens.

Time callbacks and habit recalculation stay outside the shared-head read scope. Identity calculation rechecks final source qualifications and exact SQL-keyed raw record bytes after recalculation, so a recall or revision during a clock callback cannot silently reuse the earlier source result. No source result or identity is reused across model calls, awaits or later validations.

The built-in tools getter similarly takes one local settings snapshot while synchronously constructing its definitions. The next getter call reads settings again; no tool collection or permission result is cached across calls or across extension getters.

## Local validation scope

A synthetic comparison with 24 registered sources and 1,000 authority rows required 168 complete chain traversals through the individual source path versus 3 through the fixed batch. Five same-process observations were approximately 367–409 ms versus 34–40 ms. These are measurements of that synthetic read path, not production CPU percentages or end-to-end latency guarantees. Regression tests assert bounded traversal counts rather than timing thresholds and cover fresh recall, source identity mismatch, tampering, write exclusion, exception cleanup and nested-transaction fallback.

The authority scan itself remains linear in chain length. These changes remove redundant scans within one synchronous batch; they do not skip chain validation, weaken recovery gates, or establish that all remaining CPU and page-latency costs have been resolved.
