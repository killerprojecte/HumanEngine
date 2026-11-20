package botruntime

// LocalHistory projects the most recent retained bodies, not the full view.
// The projector owns its output; the immutable retained inputs are borrowed.
func (r *Runtime) LocalHistory(sceneID string) ([]*Object, error) {
	s := r.state(sceneID)
	if s == nil {
		return []*Object{}, nil
	}
	v := r.trimPublished(s)
	rows := make([]*Object, 0, BrowserHistoryWindow)
	for block := v.history; block != nil && len(rows) < BrowserHistoryWindow; block = block.previous {
		for i := len(block.rows) - 1; i >= 0 && len(rows) < BrowserHistoryWindow; i-- {
			rows = append(rows, block.rows[i])
		}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	recalled := []string{}
	for _, row := range rows {
		if id := idOf(row); v.recalled[id] {
			recalled = append(recalled, id)
		}
	}
	if len(rows) == 0 {
		return rows, nil
	}
	out, err := r.services.Authority.ProjectDisplay(v.scene, rows, recalled)
	if err != nil {
		return nil, err
	}
	for _, row := range out {
		_ = row.Set("origin", "history")
	}
	return out, nil
}
