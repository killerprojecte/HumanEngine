# Isolated web-read runtime candidate

This is a source-only implementation checkpoint, not a production-enabled or
fully compatible web-read service. Experimental runtime operations require the
`web_read_candidate` build tag. Default guest builds continue to return
`unknown_plugin_operation` for those operations. The default application does not register this
manager service or replace distributed WASM artifacts. A separately tagged,
explicitly selected installation is prepared in [INSTALLATION-CANDIDATE.md](INSTALLATION-CANDIDATE.md); it is not production admission.
The untagged application rejects the binding through `ProductionAdmission`. The
generic manager still requires an explicit runtime factory before registration.

The generic `BindCallWithAdmission` API separates queue admission cancellation
from an admitted call's generation lifetime. Existing `wasm.go`, `BindCall`, and
`Call` are unchanged. Physical owners must still join admitted work; the API does
not make arbitrary callbacks that ignore their lifecycle stoppable.

## Legacy network and DNS validation

The following results describe the earlier isolated network/DNS checkpoints,
not this newer manager-installation candidate or its cache-generation changes.
Those earlier focused validations passed default and candidate race tests and vet, actual default
and candidate WASM execution, new admission/cancellation/unload tests, and the
unchanged existing BindCall regression. Differential observations include 15 TCP
fixtures, 26 service fixtures, late DNS completion and synchronous policy ordering.
Fixtures are synthetic. Frozen input hashes matched before and after validation.
These checks do not establish full backend/frontend or source-free app parity.

## Remaining work

- HTTP 101 behavior remains unresolved. Candidate sentinel behavior is not
  compatible production behavior.
- A direct libc DNS adapter is implemented, with Linux runtime validation of
  native completion, cleanup and source hostname-admission boundaries. It remains
  opt-in and is not registered by the default/production application; see [DNS-ADAPTER.md](DNS-ADAPTER.md).
  Other platform targets are not runtime-validated. Cancellation cannot interrupt
  an admitted OS lookup, so physical shutdown is not bounded if libc/NSS stalls.
- Tagged manager registration, owner cache, simulation, finalization, configuration
  and generation/re-enable integration have a candidate. Source-side configuration,
  tool-input and service contracts passed. The prior c5bd253 installation runner
  also passed native cache-generation/physical-drain race regressions and actual
  guest/network/DNS policy tests. Its Application lifecycle hit a 240-second
  fixture budget during cold policy compilation; that failed evidence is retained.
  The revised 0109fb8-based r6 Application lifecycle passed under race detection
  in 314.43 seconds, including three planner receipts, live-context generation
  rejection, physical disable and re-enable. Current vet and final hashes passed.
  The strict aggregate gate combines current app/vet evidence with seven original
  passing r4 suites after exact source/log verification, not a full fresh rerun.
  Installed network/cache authority races and production admission remain unfinished.
- Actual TLS/SNI and HTTPS downgrade differential coverage, exhaustive charset
  and framing coverage, and complete timer-race coverage remain unfinished.

Do not interpret this checkpoint as default enablement, completed compatibility,
or permission to ship the candidate reactor.

