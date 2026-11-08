package modelservice

// HasConfiguredRoute mirrors Engine's configured ordered-model list presence,
// not candidate usability or a successful provider availability probe.
func (p *Policy) HasConfiguredRoute(role string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.config.Routes {
		if r.Role == role && len(r.OrderedModelIDs) > 0 {
			return true
		}
	}
	return false
}
