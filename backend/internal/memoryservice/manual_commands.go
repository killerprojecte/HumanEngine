package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
)

func (s *Service) manualCommands(runtime memorymanual.Runtime) (*memorymanual.Command, error) {
	return memorymanual.New(s.revisions, s.owners(), s.d.Authority, runtime)
}
func (s *Service) Create(ctx context.Context, input *Object) (*Object, error) {
	c, e := s.manualCommands(memorymanual.Runtime{})
	if e != nil {
		return nil, e
	}
	return c.Create(ctx, input)
}
func (s *Service) Update(ctx context.Context, id string, patch *Object) (*Object, error) {
	c, e := s.manualCommands(memorymanual.Runtime{})
	if e != nil {
		return nil, e
	}
	return c.Update(ctx, id, patch)
}
func (s *Service) Lock(ctx context.Context, id string, locked bool) (*Object, error) {
	c, e := s.manualCommands(memorymanual.Runtime{})
	if e != nil {
		return nil, e
	}
	return c.Lock(ctx, id, locked)
}
func (s *Service) UpdateGuarded(ctx context.Context, id string, patch *Object, g memorymanual.Guard) (*Object, error) {
	c, e := s.manualCommands(memorymanual.Runtime{})
	if e != nil {
		return nil, e
	}
	return c.UpdateGuarded(ctx, id, patch, g)
}
func (s *Service) PromotePublic(ctx context.Context, input Context, id string) (memorymanual.Result, error) {
	c, e := s.manualCommands(memorymanual.Runtime{})
	if e != nil {
		return memorymanual.Result{}, e
	}
	return c.PromotePublic(ctx, memorymanual.Context{SceneID: input.SceneID, Simulation: input.Simulation}, id)
}
