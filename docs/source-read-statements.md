# Fresh source reads with reusable compiled statements

`SourceEventStore` reuses private handles for its five fixed read queries. `Store`
does the same for scene permissions and recall tombstones. Every call still runs
SQL, decodes fresh data and executes the original recovery/authority callbacks in
their original order. The shared identifier schema avoids constructing the same
Zod schema for every source lookup.

Only compiled statements survive calls. There is no metadata, table-existence,
permission, source revision, recall result or eligibility cache. SQLite schema
revalidation stays enabled. SQL failure drops the handles. Reentrant execution
uses the original prepare path rather than sharing an executing statement, so the
original busy-connection failure is preserved. Each database/store owns its own
handles, and callers never receive them.

## Eligibility boundary

After an awaited delivery tool, the synchronous outcome description uses one
fresh eligibility result for status, reason and next step. Those fields are
internally constructed plain data with no intervening callback or await. The
immediately following eligibility gate still rechecks before output is observed
or projected, and all model, source, send and asynchronous boundaries keep their
existing checks. This is not a turn-wide eligibility cache.

We intentionally retain repeated source authority callbacks inside public read
methods: injected callbacks can mutate state or reenter. Removing them would
change the safety boundary rather than merely removing SQL compilation.

## Verification

- `tests/fixed-read-statements.test.ts`: fresh writes and rollback, isolated row
  objects, schema changes and recreation, SQL failure, closed connections and
  reentrant callbacks.
- `tests/source-event-read-statements.test.ts`: fixed preparation count while all
  authority callbacks execute, callback-time tombstone changes, source edits,
  malformed heads, missing/partial schemas, fresh scene permissions and recalls.
- `tests/runtime-eligibility-outcome.test.ts`: coherent synchronous outcome and
  immediate fresh denial before revoked tool output is projected or reused.

Synthetic complete-path validation also exercises actual `EmbeddingIndex.search`
with local stub embeddings and real SQLite ranking, and an incoming Engine turn
with registered habit sources, model/adapter stubs and a confirmed send receipt.
These checks cover the paths in the CPU profile without treating different
production captures as an A/B test. Compiled-handle reuse reduces preparation;
it does not remove the remaining SQL reads, JSON parsing or graph traversal.
