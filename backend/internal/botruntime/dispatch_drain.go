package botruntime

// dispatchRead is an externally retained beforeTransmit callback boundary, not
// merely turn-local synchronous work. An adapter can call it after logical send
// cancellation; reject post-close calls before touching authority, and count
// already admitted reads until their actual callback returns.
func (r *Runtime) dispatchRead(read func() (bool, error)) (bool, error) {
	done, ok := r.beginOperation()
	if !ok {
		return false, code("runtime_closed")
	}
	defer done()
	return read()
}
