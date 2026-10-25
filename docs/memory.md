# Scoped memory and vector retrieval

`MemoryService` uses the existing `Store.db` SQLite connection and loaded stable
sqlite-vec extension. It stores distilled records, provenance, deletion
fingerprints, and rebuildable vectors, not a persistent raw-chat cache. It does
not modify code, plugin files, or persona templates.

## Scope and APIs

- `list(context, personId?)` and `search(context, query, personId?, vectorOptions?)`
  apply the scene boundary in SQL, before ranking. Person lookup is strictly
  `(sceneId, personId)` and never returns shared knowledge.
- `context.simulation` allows only explicitly shared public knowledge and strips
  its private provenance. Person lookup returns nothing. `propose` immediately
  rejects simulation before any business-table write.
- `propose(context, candidate)` creates or merges scene-scoped learning. The core
  supplies `sourceEvidence`, not the model. Each source must be current-scene,
  available, nonbot, and not recalled; the scene's current learning switch is
  re-read inside the committing transaction. This prevents an old model result
  from committing after a switch-off or an observed recall.
- `create`, `update`, `lock`, `delete`, and `adminList` are **administrator-only**
  methods. They are not learning tools. Only explicit administrator creation can
  publish shared knowledge in this version; the administrator must confirm that
  the content itself is public and contains no private/person facts. Merely
  removing names from an internal project summary does not make it public.
- `recall(sceneId, messageId)` persists the source tombstone and invalidates or
  reassesses derived records in one transaction. The core also needs its own
  generation/tool/send and rule/meme dependency handling.

Tool contexts must be created by the runtime, never populated from a model's
arbitrary `sceneId`, `sourceEvidence`, or simulation flag. A trusted plugin with
host database access is not a security sandbox.

## Evidence and manual precedence

New automatic memories start with bounded confidence. A duplicate proposal merges
unique message sources; repeated bot output or repeated messages do not count as
new independent corroboration. A distinct source sender may strengthen confidence
slightly, but this is an evidence heuristic, not proof of truth. The runtime must
avoid treating forwarded material as an independent source merely because the
forwarder is a different account. Claims, uncertainty, and disagreements should
remain explicit in the content and kind.

Manual edits and locks prevent later automatic overwrite. An automatic correction
may supersede an unlocked learned record only in the same scene/person scope.
Editing content drops its vectors and suppresses immediate relearning of the old
text; deletion removes vectors and source relations and retains a scope/person
content fingerprint. The fingerprint is an exact normalized-content safeguard,
not a promise to detect arbitrary semantic paraphrases.

Observed recall removes the affected source. A record with no remaining evidence
becomes invalidated and loses vectors. A record with other evidence becomes
disputed and loses confidence, while superseded/invalidated records never become
active again as a side effect. Recall is **not** learned as proof that the content
is false. Remaining independent evidence and an administrator can resolve it.
The minimal persistent source tombstone prevents later history from reintroducing
that source; it does not restore old generation or pending send state.

## Vector spaces and exact retrieval

Register an explicit space before indexing:

```ts
const spaceId = memory.registerSpace({
  providerId: 'provider-a',
  modelId: 'embedding-instance-a',
  apiModelName: 'actual-embedding-model',
  deployment: 'https://provider.example/v1',
  dimensions: 1536,
  preprocessing: 'plain-text-v1',
  normalized: true,
})
memory.index(memoryId, spaceId, embedding)
```

The identity includes stable instance and provider IDs **and** actual API model,
deployment, dimensions, preprocessing, and normalization. Changing any field
creates a different space. A provider that silently changes a model revision
must be represented by a new deployment/revision identifier. Dimensions alone
never establish compatibility. No model fallback selects a different vector
space automatically.

Indexing validates dimensions, nonzero vectors, finite values, and float32
representability. Normalized spaces normalize vectors before storage and query.
Retrieval first materializes only allowed active/disputed, unexpired records,
then performs sqlite-vec exact cosine distance for the chosen space. It combines
that score with lexical matches, importance, confidence, and timestamp tie-breaks.
Records not yet indexed in a new space remain available to lexical retrieval;
old and new vectors are never compared in the same search. No ANN or measured
capacity/latency promise is implied by this initial implementation.

Expired raw chat is separate from memory expiry. Records do not disappear simply
because the source's raw text is no longer available. The source identifiers
remain provenance, not a fabricated transcript.

## Focused verification

Local SQLite integration tests cover two scenes sharing a person ID, explicit
public knowledge with redacted sources, simulation table snapshots, real/nonbot
source evidence, participation-independent learning switches, manual lock/delete
precedence, same-scope corrections, recall and late-write rejection, actual
sqlite-vec cosine retrieval, scope-first ranking, distinct vector spaces, and
vector cleanup after content edits/deletion. These checks do not establish
real-world learning quality, embedding provider compatibility, or large-scale
performance. Those require representative replay and benchmark data.

## Bounded-result exact SQL ranking

The search path now computes the existing hybrid score inside SQLite and applies
`ORDER BY ... LIMIT` before crossing into JavaScript. It still examines every
eligible record for exact ranking: this is **not** an ANN index or a claim that
query work is constant-time. The materialized scope/expiry/status filter precedes
vector distance and lexical scoring; there is no global top-k followed by a scope
filter and no semantic-only shortlist that can hide a rare lexical code.

The score is unchanged: `max(lexicalHitFraction, max(0, 1 - cosineDistance)) * 0.8

- confidence * 0.1 + importance`, where importance is 0.02/0.06/0.10. A missing
vector contributes zero semantic relevance. Empty queries retain all eligible
candidates before ranking; nonempty queries require positive relevance. Selected
vector spaces never mix. A deterministic scalar retains JavaScript Unicode
lowercasing and substring semantics, because SQLite's built-in `lower()` alone
  would change non-ASCII matching. Query terms are parsed once and cached only for
  the current term string. Empty-query vector searches skip that scalar entirely.

Only the bounded winning rows (default 20, maximum 100) are decoded into full
JavaScript records and sanitized. Equal scores prefer newer `learnedAt`; exact
timestamp ties now explicitly prefer the later inserted row, replacing the old
vector query's unspecified final tie order. The lexical-only and vector paths
use the same ranking rules.

An integration oracle checks 108 combinations of scene/simulation/person scope,
Unicode and rare-code queries, lexical-only retrieval, and distinct vector
spaces against the original JavaScript scoring formula. It also places malformed
vector blobs in inaccessible and expired records to verify those records never
reach the cosine function. Existing recall, source, lock, deletion, and stale
vector tests remain active.

### Measured before/after (2026-10-25 UTC)

The unchanged `node --import tsx scripts/vector-benchmark.ts` completed with exit
0 after this change. Environment and workload match the recorded baseline in
[operations](operations.md#recorded-local-run-2026-10-25-utc): Node 24.22.1,
SQLite 3.53.4, sqlite-vec 0.1.9, Linux x64 Xeon Platinum 8573C, deterministic
32-dimensional vectors. Correctness assertions passed for every query; both
writer phases reported zero failures. No OS-cache eviction was attempted.

All table values are milliseconds; pairs are p50 / p95. These separate runs on a
shared host are **not a controlled speedup experiment**. The large-scope tail
improved in this run, but smaller scopes had regressions/noise. Bounded decoding
is established by the implementation; consistent production latency improvements
are not established by these measurements.

| Corpus / scoped rows | Before warm        | After warm       | Before connection cold | After connection cold | Before writer     | After writer     |
| -------------------- | ------------------ | ---------------- | ---------------------- | --------------------- | ----------------- | ---------------- |
| 1000 / 100           | 2.459 / 6.862      | 2.066 / 8.062    | 1.969 / 6.765          | 1.869 / 4.282         | 2.251 / 4.519     | 5.653 / 11.980   |
| 1000 / 900           | 17.459 / 26.564    | 9.434 / 56.988   | 13.310 / 27.551        | 11.088 / 16.457       | 12.542 / 25.188   | 5.915 / 12.105   |
| 10000 / 1000         | 7.572 / 10.981     | 26.728 / 55.169  | 14.793 / 149.113       | 23.548 / 36.419       | 12.682 / 83.375   | 11.561 / 72.673  |
| 10000 / 9000         | 132.053 / 1012.229 | 98.964 / 238.916 | 156.817 / 373.828      | 68.890 / 138.996      | 134.039 / 828.811 | 95.685 / 235.357 |

Exact captured post-change output follows. The unchanged script's legacy note
“includes JavaScript ranking” describes the baseline implementation, **not**
this post-change path: ranking now executes in SQLite and JavaScript decodes only
the selected winners. Other timing definitions remain unchanged.

<details>
<summary>Raw post-change benchmark output</summary>

```json
[
  {
    "scriptVersion": 1,
    "environment": {
      "node": "v24.22.1",
      "platform": "linux",
      "arch": "x64",
      "cpu": "INTEL(R) XEON(R) PLATINUM 8573C",
      "sqlite": "3.53.4",
      "sqliteVec": "v0.1.9"
    },
    "synthetic": true,
    "dimensions": 32,
    "rows": 1000,
    "iterations": 30,
    "seedMs": 2617.854223,
    "scopes": [
      {
        "sceneId": "small",
        "rows": 100,
        "firstQueryMs": 8.205707999999959,
        "connectionCold": {
          "samples": 5,
          "p50Ms": 1.868927999999869,
          "p95Ms": 4.281745000000228
        },
        "warm": {
          "samples": 30,
          "p50Ms": 2.0659709999999905,
          "p95Ms": 8.062172000000373
        }
      },
      {
        "sceneId": "large",
        "rows": 900,
        "firstQueryMs": 261.68914700000005,
        "connectionCold": {
          "samples": 5,
          "p50Ms": 11.087913000000299,
          "p95Ms": 16.456833999999617
        },
        "warm": {
          "samples": 30,
          "p50Ms": 9.43440499999997,
          "p95Ms": 56.98814399999992
        }
      }
    ],
    "writeInterference": [
      {
        "sceneId": "small",
        "samples": 30,
        "p50Ms": 5.653159999999843,
        "p95Ms": 11.980293999999958
      },
      {
        "sceneId": "large",
        "samples": 30,
        "p50Ms": 5.914632999999412,
        "p95Ms": 12.104749000000083
      }
    ],
    "writeStats": {
      "writes": 141,
      "failures": 0
    },
    "notes": [
      "First query is query-cold after seeding; connectionCold uses five fresh connections. Neither purges OS cache.",
      "Exact scoped search includes JavaScript ranking and JSON decoding.",
      "Concurrent writer updates one config row every 2ms; not a production workload.",
      "Synthetic vectors and local machine timings do not establish real embedding quality or deployment capacity."
    ]
  },
  {
    "scriptVersion": 1,
    "environment": {
      "node": "v24.22.1",
      "platform": "linux",
      "arch": "x64",
      "cpu": "INTEL(R) XEON(R) PLATINUM 8573C",
      "sqlite": "3.53.4",
      "sqliteVec": "v0.1.9"
    },
    "synthetic": true,
    "dimensions": 32,
    "rows": 10000,
    "iterations": 30,
    "seedMs": 15272.133183999998,
    "scopes": [
      {
        "sceneId": "small",
        "rows": 1000,
        "firstQueryMs": 143.97742599999765,
        "connectionCold": {
          "samples": 5,
          "p50Ms": 23.5483490000006,
          "p95Ms": 36.41872700000022
        },
        "warm": {
          "samples": 30,
          "p50Ms": 26.728190000001632,
          "p95Ms": 55.16892700000244
        }
      },
      {
        "sceneId": "large",
        "rows": 9000,
        "firstQueryMs": 139.894393999999,
        "connectionCold": {
          "samples": 5,
          "p50Ms": 68.89042899999913,
          "p95Ms": 138.99557099999947
        },
        "warm": {
          "samples": 30,
          "p50Ms": 98.96428799999921,
          "p95Ms": 238.91610299999957
        }
      }
    ],
    "writeInterference": [
      {
        "sceneId": "small",
        "samples": 30,
        "p50Ms": 11.56129200000214,
        "p95Ms": 72.67345600000044
      },
      {
        "sceneId": "large",
        "samples": 30,
        "p50Ms": 95.68540900000153,
        "p95Ms": 235.35654099999738
      }
    ],
    "writeStats": {
      "writes": 1917,
      "failures": 0
    },
    "notes": [
      "First query is query-cold after seeding; connectionCold uses five fresh connections. Neither purges OS cache.",
      "Exact scoped search includes JavaScript ranking and JSON decoding.",
      "Concurrent writer updates one config row every 2ms; not a production workload.",
      "Synthetic vectors and local machine timings do not establish real embedding quality or deployment capacity."
    ]
  }
]
```

</details>
