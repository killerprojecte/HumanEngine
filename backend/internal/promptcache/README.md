# Prompt membership and successful prefix reuse

This package ports `PromptContextWindows` and `PromptCacheBoundaries` from the
actual runtime. It stores only IDs, body hashes, byte sizes and successful prefix
identities, never message bodies or permission decisions. Callers must provide
freshly authorized, ordered messages for every selection.

Windows retain the source's cold suffix, append-only reuse, rollover, required
message expansion, exact byte-budget failure ordering and 256-key LRU. Disabled
windows use the existing last-24 behavior. These are source product policies,
not new protective limits. The caller supplies schema-validated positive settings;
`Options` builds the same four ordered fields as `runtime.ts`. It does not accept
arbitrary JavaScript object prototypes or options property orders. Message IDs
must be the actual message object's `id`; ordered values use `jsjson` and strings
use canonical WTF-8. Returned entries intentionally refer to the caller's current
bodies, matching the source shallow selection. The runtime owner serializes
window operations and body mutation.

The source repeatedly serializes the shrinking array. This port serializes each
candidate body once, hashes that exact representation, and computes array size
from its elements, commas and brackets. Trimming subtracts the removed bytes;
successful membership reuses already-computed hashes. UTF-16 lone surrogates,
property order and JavaScript numeric rendering stay within the exact codec.
This removes repeated payload serialization, not the inherent cost of reading
and hashing freshly authorized bodies. It does not cache authorization.

Boundary preparation computes the same old/current indexes and retains only an
exact successful prefix. Commit tickets use pointer identity and a short mutex,
so late completion cannot overwrite a newer preparation, deletion, clear or LRU
eviction. No background queue, transport operation or restart replay is created.
The ticket's indexes are caller-owned; modifying them cannot modify retained
state. Preparing a key does not refresh a different key's membership.

The actual-source generator records 1,639 transitions, complete ordered state
hashes, outcomes and errors. It covers payload edits, authority changes, target
and exact byte thresholds, UTF-16 IDs, property order, duplicates, cold/rolling
windows, stale commits and LRU eviction. Randomized coverage asserts meaningful
counts for both selection and preparation, plus commits. Source assertions and
native tests also check serialization-failure mutation order, current-body and
returned-array ownership, and captured ticket hashes. A separate native test
races 32 stale/current committers.

```
node --import tsx backend/tests/generate-prompt-cache-fixtures.mjs --check
go test -C backend -race ./internal/promptcache
go vet -C backend ./internal/promptcache
```

`promptWindowAuthority`'s live recovery gate and raw SQLite snapshot are not
implemented by this membership package. Actual runtime composition must obtain
that snapshot through the shared authority coordinator, without treating these
cached signatures as permission or silently substituting parsed defaults. No
live bot, HTTP route, provider prompt-cache capability or turn is installed here.
