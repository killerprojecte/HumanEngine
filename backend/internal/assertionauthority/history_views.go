package assertionauthority

import "errors"

// RetainedHistoryViews implements the source history page's one shared parent
// traversal. 8192 is the source page budget (not a new admission/count cap).
// Rewriting sources is specific to historicalQuery; historyFor preserves them.
func (a *Authority) RetainedHistoryViews(revisions []*Object, rewriteSources bool) ([]*Object, error) {
	if err := a.read.CheckScope(); err != nil {
		return nil, err
	}
	traversal := newParentTraversal()
	traversal.remaining = 8192
	out := make([]*Object, 0, len(revisions))
	for _, revision := range revisions {
		record := object(get(revision, "record"))
		if record == nil {
			return nil, errors.New("assertion_record_required")
		}
		proofs, err := fieldArray(revision, "supports")
		if err != nil {
			return nil, err
		}
		proofs, err = a.filterHistorySupports(object(get(record, "scope")), proofs, traversal)
		if err != nil {
			return nil, err
		}
		if rewriteSources {
			record = record.Clone()
			sources, err := proofSources(proofs)
			if err != nil {
				return nil, err
			}
			_ = record.Set("sources", sources)
		}
		if rewriteSources {
			record, err = a.WithObservation(record, proofs)
			if err != nil {
				return nil, err
			}
		}
		copy := revision.Clone()
		_ = copy.Set("record", record)
		_ = copy.Set("supports", proofs)
		out = append(out, copy)
	}
	return out, nil
}
