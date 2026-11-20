package botruntime

// PreviewMessages projects one retained scene snapshot without copying topics
// or entering the scene owner. Managed row positions are optional, private
// proofs taken from the same ordered records being projected.
func (r *Runtime) PreviewMessages(sceneID string, managedEnabled bool) ([]*Object, map[int]struct{}, error) {
	s := r.state(sceneID)
	if s == nil {
		return []*Object{}, nil, nil
	}
	v := r.trimPublished(s)
	if !managedEnabled || v.history == nil || v.scene.Kind == "simulation" {
		rows, err := r.projectViewMessages(v)
		return rows, nil, err
	}

	bodies := make([]*Object, v.history.count)
	var managed map[int]struct{}
	at := len(bodies)
	for b := v.history; b != nil; b = b.previous {
		at -= len(b.records)
		for i, record := range b.records {
			bodies[at+i] = record.body
			if proof := record.managedMedia; proof != nil && proof.matches(v.scene) && !v.recalled[idOf(record.body)] {
				if managed == nil {
					managed = make(map[int]struct{})
				}
				managed[at+i] = struct{}{}
			}
		}
	}
	recalled := displayRecallIDs(v)
	if managed != nil {
		_, adapterPresent := r.adapters.Load(v.scene.AdapterID)
		owner, bindingSupported := r.services.Authority.(BoundDisplayAuthority)
		if adapterPresent && bindingSupported {
			rows, bound, err := owner.ProjectBoundDisplay(v.scene, bodies, recalled)
			if !bound {
				managed = nil
			}
			return rows, managed, err
		}
	}
	rows, err := r.services.Authority.ProjectDisplay(v.scene, bodies, recalled)
	return rows, nil, err
}

func (r *Runtime) projectViewMessages(v *sceneView) ([]*Object, error) {
	return r.services.Authority.ProjectDisplay(v.scene, bodyRows(v.history), displayRecallIDs(v))
}

func displayRecallIDs(v *sceneView) []string {
	recalled := make([]string, 0, len(v.recalled))
	for id := range v.recalled {
		recalled = append(recalled, id)
	}
	return recalled
}
