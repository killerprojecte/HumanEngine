package replyplan

// ReadonlyJSON is a read-only view of an owned, ordered JSON tree. Callback
// consumers cannot obtain a mutable alias. Copy explicitly returns an independent
// tree for consumers that need to construct a different projection.
// It is not a security boundary against unsafe or reflection-based mutation.
type ReadonlyJSON struct{ value any }

func (v ReadonlyJSON) Get(key string) ReadonlyJSON {
	if !has(v.value, key) {
		return ReadonlyJSON{Undefined}
	}
	return ReadonlyJSON{get(v.value, key)}
}
func (v ReadonlyJSON) At(index int) ReadonlyJSON {
	a := array(v.value)
	if index < 0 || index >= len(a) {
		return ReadonlyJSON{Undefined}
	}
	return ReadonlyJSON{a[index]}
}
func (v ReadonlyJSON) Len() int {
	if o := object(v.value); o != nil {
		return o.Len()
	}
	return len(array(v.value))
}
func (v ReadonlyJSON) Keys() []string {
	if o := object(v.value); o != nil {
		return o.Keys()
	}
	return nil
}
func (v ReadonlyJSON) String() (string, bool)     { s, ok := v.value.(string); return s, ok }
func (v ReadonlyJSON) Number() (float64, bool)    { n, ok := v.value.(float64); return n, ok }
func (v ReadonlyJSON) Boolean() (bool, bool)      { b, ok := v.value.(bool); return b, ok }
func (v ReadonlyJSON) IsNull() bool               { return v.value == nil }
func (v ReadonlyJSON) IsUndefined() bool          { return isUndefined(v.value) }
func (v ReadonlyJSON) IsArray() bool              { return isArray(v.value) }
func (v ReadonlyJSON) IsObject() bool             { return object(v.value) != nil }
func (v ReadonlyJSON) Copy() any                  { return clone(v.value) }
func (v ReadonlyJSON) Stringify() (string, error) { return stringify(v.value) }
func (v ReadonlyJSON) ProjectedStringify(projection *ContextProjection, trailingFields []string) (string, error) {
	return projection.StringifyTrailing(v.value, trailingFields)
}
