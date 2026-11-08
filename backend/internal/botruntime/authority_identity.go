package botruntime

import "reflect"

// UsesAuthority verifies a composition boundary without exposing the authority
// or acquiring runtime/database locks. Only the same concrete pointer capability
// matches; equivalent configuration or another pool is not ownership proof.
func (r *Runtime) UsesAuthority(expected Authority) bool {
	if r == nil || expected == nil || r.services.Authority == nil {
		return false
	}
	actual, wanted := reflect.ValueOf(r.services.Authority), reflect.ValueOf(expected)
	return actual.Kind() == reflect.Pointer && wanted.Kind() == reflect.Pointer && actual.Type() == wanted.Type() && actual.Pointer() == wanted.Pointer()
}
