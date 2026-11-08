package assertionauthority

// ValidParent is the source admission entrypoint. Each call gets the same fresh
// provenance budget as the source method's default parameters.
func (a *Authority) ValidParent(scope *Object, id string) (bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return false, e
	}
	return a.validParent(scope, id, positiveCache{}, &budget{4096})
}
