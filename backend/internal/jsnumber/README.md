# Source-compatible numeric operations

## Round 3 acceptance

The combined candidate passed ordinary tests, race tests and vet for jsnumber,
habitservice, recoverydomains and memoryservice. Actual source regeneration
checks passed: 8,114 frozen pow cases, 53 live traces / 247 steps, seven actual
Engine recall traces, and eight offline decay cases. Frozen and same-host pow
oracles both passed; the latter did not replace the former. Default C compilation
passed and fast-math/finite-math builds were rejected as intended. Independent
review verified all 28 candidate files match the tested snapshot.

This acceptance supersedes earlier round1/round2/pending validation notes below.
It covers the concrete domain and documented recall subsequence, not the full
robot caller graph, all HTTP routes, arbitrary libc/CPU combinations, or production
deployment. Aggregate CI on the published commit remains required.


`Pow` is shared by live `habitservice` and offline `recoverydomains`. Both
persist magnitudes and subsequently include the raw record in trusted identity
hashes. Go `math.Pow` differs from the source by ULPs on fractional-day decay;
round 2's four exact persisted-JSON tests demonstrated the defect.

## Actual source path and dependency

- Node v24.22.1, V8 13.6.233.17-node.58.
- `node --v8-options` reports `--use-std-math-pow` as the default.
- V8 `src/numbers/ieee754.cc` therefore uses its ECMAScript special-case wrapper
  around `std::pow`, not `base::ieee754::legacy::pow` in `src/base/ieee754.cc`.
- Oracle host: Linux x64, Debian glibc 2.41-12+deb13u4, linked
  `/lib/x86_64-linux-gnu/libm.so.6`. The Node executable's `ldd` output confirms
  that shared library. glibc dispatches math based on host CPU capabilities.
- This package calls native `pow` after the same NaN/Infinity/square/square-root
  handling. The library is the same one used by C++ `std::pow` on this host.
  There is no subprocess, embedded JavaScript, or Node runtime dependency.
- CGO is required, as it already is for the backend SQLite build. There is no
  non-CGO `math.Pow` fallback that would silently reintroduce wrong identities.
  Do not enable fast-math or rewrite `pow(2,y)` into `exp2(y)`.
  The C wrapper rejects `__FAST_MATH__` and nonzero `__FINITE_MATH_ONLY__`
  at compile time, so those external CGO flags cannot silently remove
  NaN/Infinity/signed-zero handling.

The small wrapper is adapted from
[Node v24.22.1 / V8](https://github.com/nodejs/node/blob/v24.22.1/deps/v8/src/numbers/ieee754.cc).
Its exact source is retained in `testdata/v8-pow-wrapper.cc.txt`, and the full
V8 BSD license is in `LICENSE-V8`. No glibc/Arm implementation is copied or
vendored. Binary distributions must also include `LICENSE-V8` (copyright,
conditions, and disclaimer) in their accompanying notices/documentation; keeping
it only in the development source tree does not satisfy that requirement.
The operating system supplies libm under its existing terms.

## Exact fixtures and limitations

`testdata/pow-node.json` freezes 8,114 actual `x ** y` results with input/output
binary64 bits. It covers signed zero, infinities, quiet/signaling NaN inputs,
36 directed raw-bit signaling base/exponent pairs (including exponents
0, -0, 1, -1, 2, 0.5 and both infinities), negative bases and integer/non-integer
exponents, normal/subnormal transition,
overflow/underflow neighbors, huge/tiny exponents, near-one bases, table-cell
halfway neighbors, default/custom habit decay and deterministic broad samples.
The generator decodes directed signaling operands from bit strings into scalar
locals and checks their bits before exponentiation; it does not put them into
a numeric array first. The original 8,078 operand/result pairs are unchanged.
Every non-NaN result must match all 64 bits. NaN is compared by classification,
not non-semantic payload bits. This is not proof over all binary64 pairs.

`TestActualNodePowBits` always checks the frozen source fixture. Additional
`TestHostNodePowBits` reads `HE_POW_ORACLE` when supplied, comparing against an
actual Node run on the deployment/CI host. The latter never replaces the frozen
check, so a host's different libm cannot make compatibility drift disappear by
regenerating expectations. Both tests and the live persisted JSON tests must
pass; a same-host pass and frozen failure means wrapper parity but migration
ABI drift, not permission to add tolerances.

Node-only generation and `--check` passed on the original oracle host. Native
CGO tests, compiler-rejection tests, and other-libc runs are pending. Pinning Node alone does not pin libm.
Ubuntu CI may use another glibc version, and other CPUs can select a different
math implementation. There is no assertion that arbitrary hosts return the
same bits. First run the strict tests on the target; if they differ, reconcile
the deployment image/libc/CPU numeric ABI with the original Node deployment
rather than rounding results or approving new fixture bytes.

From `backend`, with Node v24.22.1 and CGO/toolchain available:

```
node tests/generate-js-pow-fixtures.mjs --check
node tests/generate-js-pow-fixtures.mjs --host-output /tmp/he-pow-host.json
HE_POW_ORACLE=/tmp/he-pow-host.json CGO_ENABLED=1 GOMAXPROCS=1 \
  go test -p=1 -tags sqlite_fts5 -count=1 ./internal/jsnumber
CGO_ENABLED=1 GOMAXPROCS=1 go test -p=1 -tags sqlite_fts5 -count=1 \
  ./internal/habitservice ./internal/recoverydomains ./internal/memoryservice
```

The first regeneration check intentionally requires the original Linux x64,
glibc 2.41 oracle host. Use `--host-output` on another host without changing the
committed oracle, and run the Go tests (including the frozen test) there.
Functional limits above are platform compatibility boundaries, not new product
resource caps. Array lengths in the fixture generator only size the test sample.

After the normal CGO checks, builds with either `CGO_CFLAGS=-ffast-math` or
`CGO_CFLAGS=-ffinite-math-only` must fail with the explicit HumanEngine numeric
parity compiler error. These are expected rejection checks, not successful test
runs, and were not executed during static-only preparation.
