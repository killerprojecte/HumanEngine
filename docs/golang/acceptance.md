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

## Live Experience owner candidate (2026-11-08)

The native owner now includes feedback admission, mechanical policy validation,
policy review, instruction trials, positive reports, expiry, source withdrawal,
use accounting, and existing recovery-schema enrollment. Real persisted ports
share the same guarded writer as dependent mutations. Native ICU supplies the
source locale comparison; no lexical fallback was introduced.

The exact final owner patch passed its isolated native-ICU race suite and vet
with `REQUIRE_ICU=1`, without skips. Tests include actual pinned-TypeScript
policy/feedback/holdout traces, six live service scenarios and the 112-case
source/target, registered/unregistered, snapshot/non-snapshot reference matrix.
Disk-backed tests cover independent authority layout, transaction loss, durable
intent without ACK, source edits, and genuine main/authority close and reopen.
The reference matrix preserves arbitrary historical JSON material and property
order, while strict proof validation occurs only on the source's snapshot paths.

This owner checkpoint does not implement the Experience runtime-hints caller
chain or its REST routes. Those are separate active integration work. The default
command remains the read-only preview. Full exact-commit aggregate CI for this
new owner is pending publication; targeted success is not full robot parity.

## Live Meme owner and native image operations (2026-11-08)

The native owner now implements recognition, intake, analysis, thumbnail,
administration, search, feedback, references and recall. Pixel work, provider
requests and unlink run outside the SQLite authority writer. Hash recognition
uses indexed candidates rather than decoding the entire collection; per-asset
serialization entries are reclaimed after the last owner/waiter.

Coalesced observations have explicit supervised operation lifetimes. One
consumer ending its request cannot expire another consumer's task. Cancellation
is rechecked after waiting for the final writer, and cancelled analysis cannot
publish tags or privacy/sharing flags. Split durable deletion preserves intent,
ACK gaps, callback failure and commit-failure ticket consumption semantics.

Local validation passed the complete memeservice, imagecodec and recovery race
packages and their vet checks. The first recovery run lacked the isolated tree's
TypeScript dependency mount; its two source-oracle failures were preserved, and
the entire package passed after mounting the existing pinned source/dependencies.
Image validation includes 21 exact Sharp metadata/frame/thumbnail cases, all 52
existing compressImage cases and the real 40-million-pixel boundary fixtures.
Synthetic source traces cover tags/concepts, actual service operations and seven
split-durable failure/retry sequences. No real user database or paid provider was
used. This checkpoint still requires exact-commit aggregate CI acceptance.

The owner does not complete Meme REST or production Engine media/model exposure
composition. Observe task creation and asynchronous deletion require an explicit
root operation capability supplied by those callers; neither silently mints one
inside a transaction. The executable remains read-only-preview. Native binary
redistribution licensing and full application startup remain separate gates.

A related Experience identifier correction follows the pinned Zod validator's
Unicode-code-point length, including astral and lone-surrogate fixtures. Explicit
JavaScript string-length validation in other source paths remains UTF-16 based.
