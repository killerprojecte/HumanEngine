# Exact-ID memory authorization reads

`listByIds` narrows candidate retrieval, not authority. It retains the same recovery
check, global lifecycle refresh (also for an empty ID set), scene/shared scope,
status/time filters, current support/applicability, and source sanitization as `list`.
A deduplicated JSON ID set drives primary-key lookups; returned records keep existing
repository row order, without a new result cap. Simulation still sees shared public
knowledge only. Parent-proof traversal remains unrestricted by the requested ID set.

Exposure capture, commit, post-callback reauthorization, ongoing delivered guards,
and detached exact guards use their known IDs. Commit derives IDs only from private
prepared state, never from a caller-controlled object property. Every boundary obtains
a fresh authorized map, and callback/version/source changes remain invalidating.
`subjectEvidenceBatch` keeps its existing 200-ID limit and limited-coverage signal.

Search applicability candidates are narrowed by scope only, not by time/status in an
earlier statement. A record becoming time-eligible before the final ranking SQL must
not escape applicability screening. The final search predicates and ranking are unchanged.

Tests cover a 5,000-row same-scene fixture returning one PK candidate, ordering,
duplicate/missing/empty IDs, shared/private/cross-scene isolation, simulation, recall,
time-boundary changes, untrusted getters, and existing engine/workspace/source races.
This does not eliminate global lifecycle support scans; those require a separate design.
