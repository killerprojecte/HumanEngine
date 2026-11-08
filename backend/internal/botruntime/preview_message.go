package botruntime

// PreviewMessage selects the exact first retained source row and projects only
// that body. It never acquires the scene owner or opens an authority transaction.
// Logical retention briefly joins the per-scene publication mutex, so this is
// not a lock-free read. Trim uses O(n) record checks and temporary references,
// never unrelated body-byte cloning. SQL projection runs after that mutex exits.
func (r *Runtime) PreviewMessage(sceneID, messageID string, reads ReadAuthority) (*Object, error) {
	if reads == nil {
		return nil, code("runtime_read_authority_required")
	}
	if err := reads.Ready(); err != nil {
		return nil, err
	}
	s := r.state(sceneID)
	if s == nil {
		return nil, nil
	}
	v := r.trimPublished(s)
	selected := retainedMessage(v.history, messageID, true, 0)
	// Source finds the first ID before checking its scene, rather than skipping
	// a wrong-scene duplicate in favor of a later row.
	if selected == nil || get(selected, "sceneId") != sceneID {
		return nil, nil
	}
	recalled := []string{}
	if v.recalled[messageID] {
		recalled = append(recalled, messageID)
	}
	rows, err := reads.ProjectDisplay(v.scene, []*Object{cloneJSON(selected).(*Object)}, recalled)
	if err != nil {
		return nil, err
	}
	if err := reads.Ready(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func retainedMessage(history *messageBlock, id string, simulation bool, cutoff float64) *Object {
	var selected *Object
	retained := 0
	for b := history; b != nil; b = b.previous {
		for i := len(b.records) - 1; i >= 0; i-- {
			record := b.records[i]
			if !simulation && !(record.receivedAt >= cutoff) {
				continue
			}
			if !simulation && retained == 500 {
				return selected
			}
			retained++
			if idOf(record.body) == id {
				selected = record.body
			}
		}
	}
	return selected
}
