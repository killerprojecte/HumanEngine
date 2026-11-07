# Relation candidates

`Service` composes transaction-local relation storage and the actual assertion
authority. Required feature callbacks are supplied by the owner; there is no
production default-allow or TypeScript fallback. `WithSession` binds the exact
active child. Read-only calls borrow `assertions.Reader.Relations` on the same
publication connection, avoiding nested publication locks.

`PurgeInAssertionScope` and `AssertionCommitted` remove actual relation material
in the supplied assertion transaction. They cover only the relation part of
projection/history cleanup; callers must compose the other privacy owners.

Source-oracle tests compare raw persisted JSON, callback order, ID/clock effects,
proof intersection, review outcomes and caught SQL-error prefixes. Real authority
and lifecycle integration tests cover the separate production binding. Model
exposure, REST projection, global restore replay and production startup composition
remain follow-up work. ACK is not complete business-operation delivery.
