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
