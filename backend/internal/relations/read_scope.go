package relations

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

// ListInReadScope uses the caller's already-admitted assertion reader. It does
// not acquire a connection, refresh evidence or retain the reader. The caller
// supplies policy from that same scope, including a current writer's settings.
func ListInReadScope(reader *assertions.Reader, options Options, input Context, query ListOptions) (Listing, error) {
	c, err := inReadScope(reader, options)
	if err != nil {
		return Listing{}, err
	}
	return c.list(input, query)
}

// CurrentInReadScope requalifies one exact row without a list boundary.
func CurrentInReadScope(reader *assertions.Reader, options Options, input Context, id string) (*Object, error) {
	c, err := inReadScope(reader, options)
	if err != nil {
		return nil, err
	}
	row, err := c.read.Get(id)
	if err != nil || row == nil {
		return nil, err
	}
	return c.projection(input, row)
}

func inReadScope(reader *assertions.Reader, options Options) (*candidates, error) {
	if reader == nil || options.Enabled == nil || options.Authority.DateParser == nil || options.Authority.Compare == nil {
		return nil, errors.New("relation_read_scope_required")
	}
	if err := reader.CheckScope(); err != nil {
		return nil, err
	}
	storage, err := reader.Relations()
	if err != nil {
		return nil, err
	}
	authority, err := assertionauthority.New(reader, options.Authority)
	if err != nil {
		return nil, err
	}
	return &candidates{read: storage, options: options, current: func(ctx Context, ref *Object) (*assertionauthority.Result, error) {
		return authority.RelationAuthority(assertionauthority.Context{SceneID: ctx.SceneID, Simulation: ctx.Simulation}, assertionauthority.Reference{ID: str(get(ref, "id")), Version: num(get(ref, "version")), DeletionEpoch: num(get(ref, "deletionEpoch"))})
	}}, nil
}
