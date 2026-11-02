# Scoped habit evidence work

Habit list reads still eagerly parse and recalculate every raw administrative row.
They then apply the existing JavaScript scene/shared scope predicate before expensive
current-evidence checks. The original status/scope predicate is checked again after
those checks, and returned/shared hints still receive fresh source-event, recall and
sanitization validation. No early status/time filter is permitted: recalculation may
reactivate a disputed record.

Engine hint preparation uses the same pure scope predicate only for its private
identity worklist. It retains the full originals array and first-original map, including
first-match behavior for duplicate stored JSON IDs. It still captures identities before
list exposure and retains the model-read ticket and ongoing identity guards.

## Explicit scoped-view compatibility change

An unrelated foreign-scene evidence or private-identity error no longer blocks a scoped
view. Relevant and shared evidence still fails closed on the same error. This is a
read-only scope boundary, not permission to mutate foreign rows or skip global recovery
checks. A foreign record sharing a relevant identity is not independent of that hint:
its private identity/source binding remains subject to the existing checks.

Global eager JSON parsing/recalculation behavior is unchanged. Malformed JSON and null
records still fail before scope narrowing, including in foreign rows; errors encountered
by that initial recalculation are not suppressed. Only unrelated later evidence/identity
work is omitted. JavaScript parsing retains duplicate-key last-value behavior; no SQL
JSON scope index, canonicalization, custom SQLite function or reader fence is introduced.

Tests cover foreign legacy evidence errors, corrupt foreign source metadata, relevant/
shared fail-closed behavior, duplicate JSON scope keys, simulation, fresh recall, cross-
scope duplicate-ID first-source binding, and a disputed hint reactivating between identity
capture and exposure without being allowed to upgrade its old private identity.

The remaining global raw-row reads and recalculation are not removed. This does not
alter memory's separate global support invalidation contract.

## Matched synthetic measurement

An isolated fixture used 10,020 habits (10,000 foreign, 20 relevant), actual Engine
hint preparation and the same store/authority methods. The baseline restored the
pre-change list/identity orchestration; no source checks were stubbed. Across three
samples per arm, median elapsed preparation time was 2,099.78 ms before versus
54.44 ms scoped; median process CPU was 2,213.59 ms versus 70.27 ms. Private identity
reads fell from 10,020 to 20. Returned hints and dependency sets matched exactly.
No model invocation or platform send occurred. These are synthetic measurements,
not production latency guarantees; global raw parsing/recalculation still scales
with the number of habits.
