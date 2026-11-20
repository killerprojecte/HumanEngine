package botruntime

// LocalHistory projects only the recent tail. The proof sidecar is keyed by the
// exact owned output row so sorting and last-wins dedup cannot move its links.
func (r *Runtime) LocalHistory(sceneID string, managedEnabled bool) ([]*Object, map[*Object]struct{}, error) {
	s := r.state(sceneID)
	if s == nil {
		return []*Object{}, nil, nil
	}
	v := r.trimPublished(s)
	if v.history == nil {
		return []*Object{}, nil, nil
	}
	// The window fits in one bitset; no proof allocation for ordinary rows.
	rows := make([]*Object, min(v.history.count, BrowserHistoryWindow))
	var eligible uint32
	var recalled []string
	at := len(rows)
	for block := v.history; block != nil && at > 0; block = block.previous {
		for i := len(block.records) - 1; i >= 0 && at > 0; i-- {
			record := block.records[i]
			at--
			rows[at] = record.body
			id := idOf(record.body)
			if v.recalled[id] {
				recalled = append(recalled, id)
			} else if proof := record.displayMedia; managedEnabled && proof != nil && proof.native == nil && proof.matches(v.scene) {
				eligible |= 1 << at
			}
		}
	}
	out, bound, err := r.projectBrowserDisplay(v.scene, rows, recalled, eligible != 0)
	if err != nil {
		return nil, nil, err
	}
	var managed map[*Object]struct{}
	if bound {
		managed = make(map[*Object]struct{})
	}
	for i, row := range out {
		_ = row.Set("origin", "history")
		if bound && eligible&(1<<i) != 0 {
			managed[row] = struct{}{}
		}
	}
	return out, managed, nil
}
