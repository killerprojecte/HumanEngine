package host

import "errors"

// ProductionAdmission is an advisory candidate marker, not a wired manager
// gate. Actual isolation is the candidate build tag, unchanged default guest
// entry behavior, and absence of manager registration/distributed artifacts.
func ProductionAdmission() error { return errors.New("builtin_runtime_not_implemented") }
