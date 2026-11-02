# Memory administration response paging

`GET /api/memories` remains the unchanged complete administrative array. The
synchronous MemoryService/plugin interfaces are unchanged. The browser now uses
an additive `GET /api/memories/page` route to avoid transferring and retaining
all record bodies for a 20-row view.

## Contract

The authenticated, no-store route accepts:

- `page`: positive safe integer, default 1; response clamps to the last available page.
- `limit`: 1–100, default 20.
- `scene`: `all`, `public`, or `scene:<sceneId>`; default `all`.
- `person`: trimmed exact person ID, meaningful only for a selected scene.
- `kind`, `status`: `all` or an existing administrative kind/status.
- `q`: trimmed substring search, preserving the browser's locale-aware lowercase rule.
- `locale`: a supported BCP 47 locale, default `en`; the browser explicitly sends
  its resolved default locale for both case matching and date-string collation.

The response contains `items`, `total`, `matched`, effective `page`, `limit`, and
`personIds`. Results are newest learnedAt first, with original rowid-desc order
preserved for equal dates. Person IDs are the complete same-scene administrative
domain, independent of other filters: future, invalidated and superseded records
still contribute. Current-profile directory eligibility is not a substitute.
These compact facets remain O(number of people); this is not a strictly bounded
person-directory payload. Each record body is also unchanged, not truncated.

## Safety and compatibility boundary

Every request retains the legacy endpoint's complete, synchronous sequence:
`adminList()` (recovery gate, full SQL read and eager JSON parsing), internal-use
exclusion, then revision presentation for every remaining record. Only afterward
are filters, stable sorting and page selection applied. Off-page syntax/property
and revision-read failures are not hidden. There are no SQL JSON prefilters,
permission caches, deferred privacy obligations, new lifecycle refreshes, or
changes to recall/delete/history/vector behavior. Administration intentionally
includes inactive and future records; this route is not model-read authority.
The existing API recovery checks before and after route execution remain in place.

Each page request reads current data; this is not a cross-request snapshot cursor.
Concurrent additions or deletions may move page boundaries. The existing guarded
revision checks remain responsible for rejecting stale edits.

The UI cancels superseded reads and ignores stale completions. Selected detail
and its guarded revision are independent of the current page. Successful writes
are not retried or reclassified as uncertain merely because the subsequent page
refresh fails. Existing mutation conflict/uncertainty handling remains mandatory.

## Performance scope and evidence

This reduces response serialization, network bytes, browser JSON parsing,
retained record content, and client-side all-record filtering/sorting. Record DOM
rendering was already capped at 20. **The backend full scan and per-record revision
reads are not eliminated.** Page/filter changes now require a fresh request;
debounced search limits avoidable requests, but many page turns can do more total
backend work than the old one-time download. No sublinear backend claim is made.

Reproduce using `node --import tsx scripts/memory-admin-page-benchmark.ts`.
The fixture uses only synthetic in-memory data: 512-character bodies, 100 people,
20 page records. A local isolated run on Node 24.22.1 measured:

| Records | Legacy bytes | Page bytes | Endpoint CPU median (legacy/page) | JSON stringify CPU mean (legacy/page) |
| ------- | -----------: | ---------: | --------------------------------: | ------------------------------------: |
| 1,000   |      838,681 |     18,042 |                  16.91 / 16.51 ms |                       1.29 / 0.028 ms |
| 10,000  |    8,406,781 |     18,084 |                255.72 / 168.93 ms |                      17.68 / 0.026 ms |

Endpoint measurements include full reads/presentation and the new filtering/sort.
Stringification measurements isolate serialization of already prepared objects.
These are synthetic observations, not production profiling or latency guarantees.

`tests/memory-admin-page.test.ts` covers legacy compatibility, full presentation
ordering, off-page failures, explicit locale matching, stable order, complete
facets, bounds, recovery/authentication and stale revision guards. Browser source
and state-machine checks supplement, but do not replace, rendered-browser QA.
