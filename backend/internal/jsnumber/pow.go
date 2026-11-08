//go:build cgo

// Package jsnumber preserves the numeric behavior of the migrated Node runtime.
package jsnumber

/*
#cgo LDFLAGS: -lm
#include <math.h>

#if defined(__FAST_MATH__) || (defined(__FINITE_MATH_ONLY__) && __FINITE_MATH_ONLY__ > 0)
#error "HumanEngine JS numeric parity requires IEEE NaN/Infinity/signed-zero semantics; disable fast-math and finite-math-only"
#endif

// Adapted from Node v24.22.1 / V8 src/numbers/ieee754.cc (BSD-3-Clause).
// Copyright 2011 the V8 project authors. All rights reserved.
// See LICENSE-V8. This is the default use_std_math_pow branch, not fdlibm.
// Both operands remain function parameters: do not rewrite pow(2,y) to exp2(y),
// or enable fast-math, since that changes persisted results and their hashes.
static double humanengine_js_pow(double x, double y) {
  if (isnan(y)) return NAN;
  if (isinf(y) && (x == 1 || x == -1)) return NAN;
  if (isnan(x)) x = NAN;
  if (y == 2) return x * x;
  if (y == 0.5) {
    if (isinf(x)) return INFINITY;
    return sqrt(x + 0.0);
  }
  return pow(x, y);
}
*/
import "C"

// Pow matches the source V8 default std::pow wrapper on the same native libm
// ABI. It is intentionally not Go math.Pow: small ULP differences change
// persisted habit JSON and trusted validation identities. See README.md for
// the pinned oracle platform and deployment requirements.
func Pow(x, y float64) float64 {
	return float64(C.humanengine_js_pow(C.double(x), C.double(y)))
}
