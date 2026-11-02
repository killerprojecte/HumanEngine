# Source-event head lookup index

SourceEventStore repeatedly resolves heads by `(scene_id, message_id)`. Its original
schema had only identity-key and event-ID indexes, so registered-source checks,
collision checks, recall lookup and per-scene counts scanned every head.

The NONUNIQUE `idx_source_event_heads_scene_message` index covers these existing
scalar predicates. It does not remove duplicate rows: `registered()` still rejects
multiple matching heads and still validates every selected raw payload and identity.
No permission, source, recovery or revision check is cached or skipped.

Existing complete source-event schemas receive the index in a writable Store startup
transaction after the recovery gate. New schemas create it in the existing source
mutation initialization transaction. Absent/partial schemas are not enrolled/repaired;
partial schemas still fail on use. Read-only validators, lazy getters and `exists()`
perform no DDL.

The `idx_` name is intentional: recovery reserves `source_event_%` for its four
authoritative tables. This ordinary access index lives outside that table namespace,
so older readers remain compatible without weakening the unknown-object guard or
adding reader capabilities. Tests explicitly retain rejection of unknown objects
inside the reserved namespace.

Validation covers fresh initialization, persistent old-schema startup upgrade without
raw-value changes, read-only lookup with/without an index, duplicate/malformed heads,
transaction rollback, exact/prefix query plans and recovery-source replay behavior.

Matched synthetic benchmark (Node v24.22.1, 20,001 heads, 400 actual `registered()`
lookups per sample, four samples per arm): median elapsed lookup time 652.312 ms without the index versus
50.652 ms with it. All lookup outputs matched. The query plan changed from a table
scan to a composite-key search; index construction took 9.104 ms. These are bounded
synthetic measurements, not a production latency guarantee. Full global memory/habit
read redesign remains separate work.
