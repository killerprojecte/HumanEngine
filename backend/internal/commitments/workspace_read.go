package commitments

// Workspace methods retain the exact ReadCurrent callback lifetime. These are
// narrow real owners, not arbitrary query or substitute authorization ports.
func (s *ReadService) AssertReady() error {
	if e := s.check(); e != nil {
		return e
	}
	return s.reader.ready()
}
func (s *ReadService) CurrentAssertion(ctx Context, ref Reference) (*Assertion, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.current(ctx, ref)
}
func (s *ReadService) SourceHead(scene, message string) (*Object, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.sourceHead(scene, message)
}
func (s *ReadService) Heads(scene string) ([]any, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.reader.Heads(scene)
}
func (s *ReadService) Enabled(ctx Context) (bool, error) {
	if e := s.check(); e != nil {
		return false, e
	}
	return s.enabled(ctx)
}
