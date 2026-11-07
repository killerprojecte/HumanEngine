package assertionauthority

import "errors"

// AnalyzeSupported finishes a synchronous provenance-only batch before its
// caller can mutate anything. Positive results are private to this call; cycle-
// dependent negative results are never memoized. Every root receives the source
// implementation's fresh traversal budget and empty path. No callback, reusable
// cache, checkpoint or current-time/version applicability enters this boundary.
func (a *Authority) AnalyzeSupported(records []*Object) ([]bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return nil, e
	}
	cache := positiveCache{}
	results := make([]bool, len(records))
	for i, record := range records {
		if record == nil {
			return nil, errors.New("assertion_record_object_required")
		}
		ok, e := a.supported(record, path{}, cache, &budget{4096})
		if e != nil {
			return nil, e
		}
		results[i] = ok
	}
	return results, nil
}
