# Go migration acceptance record

The feature branch is not yet a production replacement. The executable still
uses read-only preview mode; completed packages and checkpoints do not establish
that all runtime services, HTTP routes, plugin capabilities or deployment paths
are wired together.

## Exact-head accepted checkpoints

- Native installation and genuine sqlite-vec bundle: `f373500da7dcf3e149554269b112dfb9c1659edc`, [CI 42451858983](https://github.com/killerprojecte/HumanEngine/actions/runs/42451858983), success.
- Native provider transport and durable budget ownership: `a430c54fc1be2b768c21677ae80ed4bf95f89c84`, [CI 42452719077](https://github.com/killerprojecte/HumanEngine/actions/runs/42452719077), success.
- Quarantined recovered-history lifecycle: `069ca838dc93b8cc3e62fc4b278a8fd34d1b61fd`, [CI 42453854997](https://github.com/killerprojecte/HumanEngine/actions/runs/42453854997), success.

- Whole quarantined reconciliation and real offline domain owners: `ce59b8227c1813331fb4c903572f97bf176977aa`, [CI 42456985346](https://github.com/killerprojecte/HumanEngine/actions/runs/42456985346), success.

These runs include the default full race/vet, actual TypeScript source fixture
regeneration, builtin WASM/bundle checks, SQLite-profile and source-free proofs,
and the pinned native ICU full test/vet/source-free combination. The native
installation check also requires actual named-timezone execution.

The corresponding implementation sections describe what was known when each
checkpoint was prepared. Their historical "CI pending" statements are superseded
by the exact-head successful runs listed here, not by an unverified local claim.

## Claims not established by these checkpoints

Whole-application readiness, all REST mutations, complete live learning/media
services, all plugin capabilities and end-to-end startup/restore still require
composition and acceptance. The native image codec and full recovery runner are
being developed separately. The image library's complete distribution materials
and runtime packaging are not established by successful pixel fixtures.

Performance evidence is scoped. Shared coordinator tests avoid repeated audits
for known controlled writes, but first-open and external-change audits still
cost linear history work. Slow-client publication contention and full HTTP/worker
load behavior remain open; these checkpoints do not prove the whole project has
no remaining performance problems.
