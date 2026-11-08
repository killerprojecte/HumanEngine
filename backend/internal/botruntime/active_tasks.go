package botruntime

// ActiveTasks reports the same published task count used by participation.
// No scene lock, history read, scheduler reservation or cleanup is performed.
func (r *Runtime) ActiveTasks() int {
	count := 0
	r.scenes.Range(func(_, value any) bool {
		s := value.(*sceneState)
		if v := s.view.Load(); v != nil {
			count += v.active
		}
		return true
	})
	return count
}
