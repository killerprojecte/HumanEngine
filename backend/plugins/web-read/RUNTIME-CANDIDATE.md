# Isolated web-read runtime candidate

This is a source-only implementation checkpoint, not a production-enabled or
fully compatible web-read service. Experimental runtime operations require the
`web_read_candidate` build tag. Default guest builds continue to return
`unknown_plugin_operation` for those operations. This change does not register a
manager service, replace distributed WASM artifacts, or enable candidate behavior.
`ProductionAdmission` is an advisory marker only, not an enforced registration gate.

The generic `BindCallWithAdmission` API separates queue admission cancellation
from an admitted call's generation lifetime. Existing `wasm.go`, `BindCall`, and
`Call` are unchanged. Physical owners must still join admitted work; the API does
not make arbitrary callbacks that ignore their lifecycle stoppable.

## Validation scope

Focused validation passed default and candidate race tests and vet, actual default
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
  opt-in and is not registered with the manager; see [DNS-ADAPTER.md](DNS-ADAPTER.md).
  Other platform targets are not runtime-validated. Cancellation cannot interrupt
  an admitted OS lookup, so physical shutdown is not bounded if libc/NSS stalls.
- Whole-manager registration, owner cache, simulation, finalization, configuration,
  generation/re-enable lifecycle and integration remain unfinished.
- Actual TLS/SNI and HTTPS downgrade differential coverage, exhaustive charset
  and framing coverage, and complete timer-race coverage remain unfinished.

Do not interpret this checkpoint as default enablement, completed compatibility,
or permission to ship the candidate reactor.

