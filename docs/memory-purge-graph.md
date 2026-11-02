# Operation-local provenance purge graph

`ProofInvalidation.purgeHistoryDependents` snapshots the current-proof reverse graph
once per operation instead of rereading every record and proof for each descendant.
`refreshSupport` shares one local graph across its unsupported roots. The graph is
never retained across calls, mutation/authority boundaries, or transactions.

Production purge ports remove history, projections and working-set metadata, or mark
revision rows dirty; they do not mutate current proofs during traversal. Current-proof
immutability during this synchronous operation is an explicit requirement. Historical
links remain live: every node discovers its historical dependents immediately before
its own purge effects, as before.

An iterative DFS preserves history-first child ordering, then current repository order,
including shared descendants and cycles. `seen` and `alreadyPurged` retain their exact
semantics: projection removal and surviving-row touch still occur for already-purged
history, and no descendant receives a duplicate epoch within the traversal.

This changes repeated full current-proof work from O(records × descendants) to one
O(records + proof edges) snapshot plus traversal. It also removes recursive stack depth
from purge. It does not remove lifecycle refreshes, cache authorization decisions, or
weaken source revocation, OR-proof survival, history deletion, or transaction rollback.

Validation adds a 15,000-node cyclic chain with one records read and 15,000 proof reads,
independent unsupported roots sharing one snapshot, historical-only/shared descendants,
and exact DFS event ordering. Existing persistent history lifecycle tests cover privacy,
restart, and rollback behavior. Global support refresh/read-scope optimization remains
separate work; this patch does not claim to bound every memory read.
