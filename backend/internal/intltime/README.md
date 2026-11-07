# Participation-window hour

`Hour` implements the exact source `Intl.DateTimeFormat('en', {hour:
'numeric', hourCycle:'h23', timeZone})` projection. The shared timezone census
comes from Node 24.22.1, ICU 78.3 and tzdata 2026e, including aliases and
case-insensitive names. Numeric offsets and UTC use integer arithmetic.
Named zones use the same hash-verified embedded ICU data as Date.parse and
collation. Non-native builds reject named zones, rather than quietly using
host timezone rules. No Node process is involved.

The oracle covers 14,378 source results, including both Date range endpoints,
pre-epoch timestamps, historical offsets and DST transition boundaries. Native
builds must run all cases; default builds verify fixed zones and explicit native
unavailability. The source-free test also executes the native hour binary.

A calendar is opened and closed per named-zone call, matching the original
per-call formatter lifetime. This is a remaining allocation cost, not a claim
that native timezone formatting is free. UTC and numeric offsets allocate no
calendar. No cache size, input cap, or additional admission policy is introduced.
