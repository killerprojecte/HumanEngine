# Fixed memory list reads

`New(reader, options).List(input)` implements `readMemoryList` and its scope
filter, borrowing one existing assertions reader. It performs SQLite JSON/time
filtering, exact-ID JSON deduplication, descending rowid ordering, then actual
fresh assertion support/applicability and sanitization. Support SQL statements
are prepared within the same capability; no authority answers are retained.

The owner may supply an active bound write child or one verified read scope.
The service never acquires another connection, creates schema, invokes a model,
imports legacy history, or replays startup work. Expired or suspended readers
fail through their original capability checks.

This is not the outer `MemoryService.list/listByIds`: real refreshSupport with
knowledge/semantic/projection privacy effects remains required upstream.
Returned-reference capture and its revision transaction/legacy import remain a
separate unfinished owner. No no-op maintenance callbacks substitute for them.
