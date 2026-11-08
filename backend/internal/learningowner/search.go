package learningowner

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (o *Owner) Search(ctx context.Context, scene, query string) (out []*Object, err error) {
	empty := []*Object{}
	if ctx.Err() != nil {
		return empty, nil
	}
	ticket, e := o.d.Read.Require(context.WithoutCancel(ctx), scene)
	if e != nil {
		return nil, e
	}
	guard := func() error { return o.d.Read.Assert(context.WithoutCancel(ctx), ticket) }
	lexical := func() ([]*Object, error) {
		if e := guard(); e != nil {
			return nil, e
		}
		return o.d.Memory.Search(ctx, memoryservice.Context{SceneID: scene}, query, "", nil)
	}
	state, e := o.indexState(ctx)
	if e != nil {
		o.log("warn", "query_embedding_unavailable")
		return lexical()
	}
	active := state.active
	if !indexUsable(state, active) {
		return lexical()
	}
	result, e := o.d.Models.Embeddings(o.readGuard(ctx, ticket), state.selected.model, []string{query})
	if e != nil {
		if e.Error() == "embedding_dimensions_mismatch" {
			_ = o.markIndexIncompatible(context.WithoutCancel(ctx), str(active, "generationId"))
		}
		if ctx.Err() != nil {
			return empty, nil
		}
		o.log("warn", "query_embedding_unavailable")
		return lexical()
	}
	if ctx.Err() != nil {
		return empty, nil
	}
	if e = guard(); e != nil {
		return nil, e
	}
	err = o.d.Authority.WithWriteSession(ctx, o.d.Main, func(ws *recovery.WriteSession) error {
		if !o.d.Read.ValidInSession(context.WithoutCancel(ctx), ticket, ws) {
			return errors.New("memory_read_revoked")
		}
		current, e := readIndexState(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) }, false)
		if e != nil {
			return e
		}
		var vector *memoryservice.SearchVector
		if indexUsable(current, active) {
			if len(result.Vectors) == 0 || get(active, "dimensions") != float64(len(result.Vectors[0])) {
				if e = saveIndexValue(ws, "embedding.incompatible", str(active, "generationId")); e != nil {
					return e
				}
			} else {
				vector = &memoryservice.SearchVector{SpaceID: str(active, "spaceId"), Vector: result.Vectors[0]}
			}
		}
		out, e = o.d.Memory.SearchInSession(ctx, ws, memoryservice.Context{SceneID: scene}, query, "", vector)
		return e
	})
	if err != nil {
		if ctx.Err() != nil {
			return empty, nil
		}
		o.log("warn", "query_embedding_unavailable")
		return lexical()
	}
	if e = guard(); e != nil {
		return nil, e
	}
	return
}
