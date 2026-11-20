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
	rows, bound, err := r.projectBrowserDisplay(v.scene, bodies, displayRecallIDs(v), managed != nil)
	if !bound {
		managed = nil
	}
	return rows, managed, err
}

// projectBrowserDisplay reuses the projection's persisted scene read when a
// retained managed receipt requires proof of its current display binding.
func (r *Runtime) projectBrowserDisplay(scene Scene, bodies []*Object, recalled []string, managed bool) ([]*Object, bool, error) {
	if managed {
		_, adapterPresent := r.adapters.Load(scene.AdapterID)
		if owner, ok := r.services.Authority.(BoundDisplayAuthority); adapterPresent && ok {
			return owner.ProjectBoundDisplay(scene, bodies, recalled)
		}
	}
	rows, err := r.services.Authority.ProjectDisplay(scene, bodies, recalled)
	return rows, false, err
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
