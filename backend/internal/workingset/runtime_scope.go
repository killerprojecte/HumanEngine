package workingset

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// ViewRuntime resolves and reads working-set metadata on a leased runtime
// snapshot or active write child. It never opens another publication or hands
// the resolver a write session.
func (s *Service) ViewRuntime(scope sourceevents.RuntimeReadScope, c Context, input []Selection, includeCold bool) (ViewResult, error) {
	bound, err := s.bindRuntimeView(scope)
	if err != nil {
		return ViewResult{}, err
	}
	return bound.view(c, input, includeCold)
}

// ViewResolution pairs exact canonical model bodies with the metadata resolved
// during the same callback. Its bodies are keyed by SelectionKey, never stored.
type ViewResolution struct {
	Snapshots       []Snapshot
	CanonicalBodies map[string]*jsjson.Object
}

// ViewRuntimeWithWitness returns bodies only for rows matching authorized exact
// selections in the configured resolver. Returned bodies are keyed by SelectionKey.
func (s *Service) ViewRuntimeWithWitness(scope sourceevents.RuntimeReadScope, c Context, input []Selection) (ViewResult, map[string]*jsjson.Object, error) {
	bound, err := s.bindRuntimeView(scope)
	if err != nil {
		return ViewResult{}, nil, err
	}
	var witness map[string]*jsjson.Object
	out, err := bound.viewCurrent(c, input, true, &witness)
	if err != nil {
		return ViewResult{}, nil, err
	}
	return out, witness, nil
}

func (s *Service) bindRuntimeView(scope sourceevents.RuntimeReadScope) (Service, error) {
	if s.bindingError != nil {
		return Service{}, s.bindingError
	}
	if s.session != nil || s.readScope != nil || s.maintenance != nil {
		return Service{}, errors.New("working_set_unbound_runtime_owner_required")
	}
	b := *s
	b.readScope = &Scope{
		main: s.main, authority: s.authority,
		check: func() error { return scope.CheckBinding(s.main, s.authority) },
		query: func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) },
		prepare: func(q string) (PreparedRead, error) {
			p, err := scope.PrepareAssertionRead(q)
			if err != nil {
				return nil, err
			}
			return runtimeScopeStatement{p}, nil
		},
	}
	return b, nil
}

type runtimeScopeStatement struct {
	sourceevents.RuntimeReadStatement
}

func (p runtimeScopeStatement) Query(args ...any) (Rows, error) {
	return p.RuntimeReadStatement.Query(args...)
}
