package botruntime

// PreviewMessages applies the browser preview projection without copying topic
// state or entering the scene owner. A scene with no published rows has an
// empty preview, including when the scene has not been configured.
func (r *Runtime) PreviewMessages(sceneID string) ([]*Object, error) {
	s := r.state(sceneID)
	if s == nil {
		return []*Object{}, nil
	}
	return r.projectViewMessages(r.trimPublished(s))
}

func (r *Runtime) projectViewMessages(v *sceneView) ([]*Object, error) {
	recalled := make([]string, 0, len(v.recalled))
	for id := range v.recalled {
		recalled = append(recalled, id)
	}
	return r.services.Authority.ProjectDisplay(v.scene, bodyRows(v.history), recalled)
}
