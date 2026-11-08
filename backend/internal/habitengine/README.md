# Concrete Engine habit calls

## Candidate acceptance

The final two-layer candidate passed native tests, race tests and vet. Independent
review verified all nine files against the tested snapshot. Source generation and
check cover 12 Engine.proposeHabit argument cases plus eight actual Engine tool
branch cases; native tests separately exercise real owned SQLite writes and the
source-edit barrier. Simulation with captured snapshots short-circuits before
database access, while missing snapshots retain the source registered-check order.
These results supersede earlier pending execution notes below. Aggregate published
CI, REST routes and full runtime/exposure composition are still required.


This package composes the actual native HabitService, source-event store/runtime
read authority, and the source-timezone JS date parser. It implements the body of
`Engine.proposeHabit` and the `habits.propose` / `habits.feedback` branches **after**
the Engine's model-tool and semantic/identity/knowledge exposure gates. It is not
a permissive replacement for those earlier gates and is not installed as an
ungated global dispatcher.

The native service persists through its actual writer, independent withdrawal
ledger and source checks. No business callback or TypeScript subprocess replaces
that implementation. A caller must supply a runtime-owned Turn, never construct
its Scene, Preview or SourceSnapshots from tool arguments. A nil snapshot means
fallback; an empty captured snapshot must not fall back. Messages are deep copied.
The first duplicate ID supplies the source author, simulation originals are
filtered out of evidence, dates use the configured source timezone, text parts
join in order, unavailable/limited messages short-circuit the actual source check.
Missing captured snapshots are rejected for registered originals. Proposal and
feedback deliberately retain their different schema/simulation/check ordering.

Schema size constraints are the existing TS product contracts (20 source IDs,
1000 UTF-16 content units), not new resource guards. Feedback source IDs have no
new length restriction. Unknown feedback properties remain ignored as in the
source branch. Proposal strict schemas reject unknown properties. Typed argument
errors currently identify the invalid-argument class; the outer tool error
presenter must preserve its established public mapping, not expose this Go code
as a claimed byte-identical Zod diagnostic.

## Tests and limits

The checked fixture generator invokes the actual pinned `Engine.proposeHabit`,
with its real Store/Engine and source validation; only the final HabitService
entry is captured to inspect its arguments. Twelve cases cover simulation,
source author lookup/duplicates, filtering, unavailable/limited evidence, Date.parse,
scene-rule/expression conversion, and schema errors. This is an argument oracle,
not evidence that writes passed. Native tests separately use real SQLite WAL and
an independent authority, exercise saved proposal, real enrollment, preview
rejection and captured-source feedback. Native execution is pending the shared
resource window; no race/vet or full integration claim is made yet.

Remaining integration: outer Engine/model-read/exposure gate composition,
runtime callback installation, learnedHints read tickets and turn dependencies,
learning persistence ordering, full recall owners and admin HTTP routes. This
package is not the complete robot or HTTP migration.

## REST implementation design

Source `src/admin.ts` has GET `/api/habits`, PATCH `/api/habits/{id}` requiring a
strict `{locked:boolean}`, and DELETE returning `{ok:true}` even if absent. It
has no POST create route. GET calls AdminList, not List/sanitize: stored author
watermarks and source arrays therefore remain in the admin response, and rowid
order plus decay recalculation must be preserved. Update returns raw record;
delete must execute actual durable `habit_delete` authority semantics.

The existing HTTP server only publishes configuration routes. Simply calling
AdminList and then writing to ResponseWriter would create a publication gap.
Calling AdminList inside Authority.Publish would recursively reacquire authority
and deadlock; its clock callback also intentionally runs after the read snapshot.
A proper extension needs a service-owned capture/recalculate/revalidate plan or
an explicitly scoped session API, preserving the source publication gate's
plan freshness checks, then enqueue immutable bytes under the final authority
reservation and drain only after releasing it. PATCH/DELETE must validate HTTP
input before entering the writer, retain post-commit uncertainty semantics, and
must not reinterpret a lost response as permission to replay a write. This is
why no raw route has been enabled in this slice. Add actual Hono route differential
fixtures and slow-client/writer/withdrawal tests before exposing those three paths.
