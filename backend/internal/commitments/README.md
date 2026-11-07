# Retained commitment privacy, not interactive commitment commands

This package owns the five real commitment tables, strict source-schema readers,
immutable revision terms, privacy pruning, exact original-proof requalification,
and recorded temporal-state redaction. It does not implement admission or claim
complete MemoryService, recovery maintenance, runtime recall, or startup parity.

Construction and reads never create tables, enroll legacy material, replay queues,
or start models. Initialize explicitly creates the three base tables in an owned
transaction. Temporal replacement lazily creates its two tables without an extra
savepoint, preserving caught-failure statement prefixes. Stores borrow the exact
active WriteSession; suspended ancestors, expired children and unrelated main or
authority bindings fail. AfterWrite is required and receives the active bound
store. Production working-set owners remain the composition owner's obligation.

Privacy maintenance ignores feature and learning flags. It preserves immutable
terms and surviving original OR alternatives, including inherited proposal AND
groups. Requalification requires an exact captured from/to ref, stable backing
fingerprint and original deep-equal proof intersection. It never follows an
arbitrary newer version. Refresh uses real assertion history/authority after the
coordinator checkpoint. ReadPrivacyRefs is only the inner source read, not the
stronger query-only validateFixedReads capture or one-use prepared authorization.

Historical declarations are never recomputed. Loss of any original kind/person
declaration or inherited proposal group nulls the entire transition's states.
Each live privacy change appends a metadata-only states=null marker, normalizes
its complete timeline, raises reader 3 and reader 10 when temporal material is
involved, and appends the real typed durable CommitmentHead/Delete intent before
local writes. Deletion still requires reader 10 when it removes a timeline even
though CommitmentDelete is a v1 event. Failed main writes preserve authority
intent prefixes and leave an ACK gap rather than pretending rollback undid them.

JSON parsing/stringification uses jsjson lossless strings and ordered objects.
Pinned Zod 4.6.7 string schema bounds count code points; explicit source .length
checks (temporal IDs) count UTF-16 units. Schema/property order, absent/null, signed
zero in deep equality, and original proof array order remain distinct. Exact
source localeCompare and date policies are injected; no host locale fallback.
The source's 64/8/201/20 product/schema widths are preserved; no new caps exist.

Storage's Put/Remember/ReplaceTimeline are trusted persistence primitives, not
admission authorization. EraseRetained is a trusted owner privacy operation, not
the interactive feature/exposure permission check. ReadTimeline likewise needs
its caller to authorize the current exposure/mutation anchor first.
