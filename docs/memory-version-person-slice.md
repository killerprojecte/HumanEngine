# Version and person checkpoint

This checkpoint follows the compatibility foundation. It is not the complete
memory/learning roadmap and does not establish real-model extraction quality.

## Implemented boundaries

- Person lists and cards use explicit stable person IDs within a selected scene;
  known nicknames are display metadata, never an identity merge rule.
- Current records and final AND/OR proofs are captured in the same SQLite
  transaction. Intermediate record writes before proof updates are not revisions.
- Manual revisions have an `admin_assertion` event category. An unavailable actor
  identity is null, never a fabricated user/chat source.
- Legacy import is lazy and idempotent. Existing IDs/status/lineage stay intact;
  unknown validity starts remain null. Recorded time is separate from occurrence
  and assertion applicability. New explicitly supplied `validFrom` is not guessed.
- Current retrieval checks the record and all required parents' applicability,
  with OR alternatives preserved. Future applicability does not permanently
  invalidate a supported record. Reconciliation and lexical/vector top-k use the
  same exclusion policy. Replyer excerpts preserve applicability and epistemic kind.
- Guarded administrator edits use expected version/epoch and a bounded,
  content-free idempotency receipt. Legacy authenticated API clients remain
  compatible; newly rendered edits carry guards.
- Recall/deletion clears historical bodies using both current and historical
  parent dependencies. Independently supported surviving records get a new safe
  current snapshot. Repeated recall is idempotent; deleted IDs cannot resurrect.

## Regression cases discovered during review

The checkpoint explicitly tests legacy quarantine followed by manual review,
parent deletion after the child's current proof was replaced, importing a legacy
verified proof before modifying it, recalled proof alternatives during normal and
lazy import, indirect OR survivors, repeated recall, transaction rollback and
restarting a persistent database. Checking only the public history response is
insufficient: tests also inspect persisted revision bodies/source indexes.

## Limits and next checkpoints

Historical parent references are still IDs, not immutable selected parent revision
references. Current authority and parent validity are rechecked, conservatively
omitting unavailable historical evidence. This is **not** complete as-of evidence
replay. Binding new proofs to parent versions, with unknown legacy parent versions,
is required before that stronger claim or temporal retrieval API is enabled.

The visible revision view is the retained, currently authorized subset. Revoked
bodies are not a hidden archive and cannot be restored through it. Existing backup
restore is not a cross-backup deletion-ledger merge; rolling back an old binary or
restoring an older database must not be advertised as deletion-safe. A future
restore migration must apply the latest external deletion ledger before reads.

No new autonomous graph expansion, strategy promotion, learning enablement,
replaying old chats, or unknown sends is introduced. Learning admission/outbox and
semantic WAIT are separate subsequent checkpoints. All tests here use synthetic
records and fake/local model receipts; no existing credentials are accessed.
