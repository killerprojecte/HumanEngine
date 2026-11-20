package botruntime

// LocalHistory projects only the recent tail. The proof sidecar is keyed by the
// exact owned output row so sorting and last-wins dedup cannot move its links.
func (r *Runtime) LocalHistory(sceneID string, mediaEnabled bool) ([]*Object, map[*Object]DisplayImageLinks, error) {
	s := r.state(sceneID)
	if s == nil {
		return []*Object{}, nil, nil
	}
	v := r.trimPublished(s)
	if v.history == nil {
		return []*Object{}, nil, nil
	}
	// Allocate the link positions only for rows carrying actual proofs.
	rows := make([]*Object, min(v.history.count, BrowserHistoryWindow))
	type positionedLink struct {
		at    int
		proof DisplayImageLinks
	}
	var eligible []positionedLink
	linker := browserImageLinker{runtime: r, scene: v.scene}
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
			} else if mediaEnabled && record.displayMedia != nil {
				if proof := linker.links(record.displayMedia); proof != (DisplayImageLinks{}) {
					eligible = append(eligible, positionedLink{at, proof})
				}
			}
		}
	}
	out, bound, err := r.projectBrowserDisplay(v.scene, rows, recalled, len(eligible) != 0)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range out {
		_ = row.Set("origin", "history")
	}
	var links map[*Object]DisplayImageLinks
	if bound {
		links = make(map[*Object]DisplayImageLinks, len(eligible))
		for _, link := range eligible {
			links[out[link.at]] = link.proof
		}
	}
	return out, links, nil
}
