package learningowner

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// IndexAdminRead applies local maintenance before publishing a snapshot. It
// never opens a provider or launches rebuild work.
type IndexAdminRead func(context.Context, func([]byte) error) (bool, error)

func (o *Owner) BindIndexAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (IndexAdminRead, error) {
	if !publisher.OwnedBy(o.d.Main, o.d.Authority) {
		return nil, errors.New("embedding_index_read_binding")
	}
	return func(ctx context.Context, emit func([]byte) error) (bool, error) {
		started := false
		// Like startup, inspect and change the lease in the same writer.
		// Only an authenticated request reaching this owner may expire it.
		err := o.d.Authority.WithWriteSession(ctx, o.d.Main, func(ws *recovery.WriteSession) error {
			rows, err := ws.Query("SELECT value FROM config WHERE key='embedding.rebuild'")
			if err != nil {
				return err
			}
			var raw string
			found := rows.Next()
			if found {
				err = rows.Scan(&raw)
			}
			if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
				return err
			}
			started = true
			if !found {
				return nil
			}
			value, err := jsjson.Parse(raw)
			if err != nil {
				return err
			}
			return o.expireIndexLease(ws, indexObject(value))
		})
		if err != nil {
			return started, err
		}
		// RefreshSupport owns invalidation, dependent history and vector removal.
		if err = o.d.Memory.RefreshSupport(ctx); err != nil {
			return started, err
		}
		err = publisher.Publish(ctx, func(conn *sql.Conn) ([]byte, error) {
			return o.indexStatus(ctx, conn, schemas)
		}, emit)
		return started, err
	}, nil
}

func (o *Owner) indexStatus(ctx context.Context, conn *sql.Conn, schemas *contract.Schemas) ([]byte, error) {
	query := func(q string, args ...any) (indexRows, error) { return conn.QueryContext(ctx, q, args...) }
	state, models, err := readIndexValues(query)
	if err != nil {
		return nil, err
	}
	// TypeScript reads persisted models through ModelConfigSchema; invalid
	// configuration presents as unconfigured without dropping the active/job.
	if models != nil {
		if config, err := schemas.ParseOrdered("ModelConfig", models); err == nil {
			state.selected, _ = selectEncoder(config)
		}
	}
	status := "unconfigured"
	if state.selected != nil {
		status = "needs_rebuild"
	}
	var indexedIDs map[string]struct{}
	if indexUsable(state, state.active) {
		status = "ready"
		indexedIDs, err = indexedMemories(ctx, conn, str(state.active, "spaceId"))
		if err != nil {
			return nil, err
		}
	}

	active := true
	defer func() { active = false }()
	reader, err := o.d.Assertions.BorrowReader(func() error {
		if !active {
			return sql.ErrTxDone
		}
		return ctx.Err()
	}, func(q string, args ...any) (assertions.Rows, error) {
		return conn.QueryContext(ctx, q, args...)
	}, func(q string) (assertions.PreparedRead, error) {
		statement, err := conn.PrepareContext(ctx, q)
		return indexStatement{ctx, statement}, err
	})
	if err != nil {
		return nil, err
	}
	eligible, indexed, err := o.d.Memory.IndexCoverage(reader, indexedIDs)
	if err != nil {
		return nil, err
	}
	const generations = "json_extract(s.value,'$.generation') IS NOT NULL"
	var retainedGenerations, retainedVectors int
	err = conn.QueryRowContext(ctx, "SELECT "+
		"(SELECT count(*) FROM vector_spaces s WHERE "+generations+"), "+
		"(SELECT count(*) FROM memory_vectors v JOIN vector_spaces s ON s.id=v.space_id WHERE "+generations+")",
	).Scan(&retainedGenerations, &retainedVectors)
	if err != nil {
		return nil, err
	}

	body := obj("state", status)
	if state.active != nil {
		_ = body.Set("active", state.active)
	}
	if s := state.selected; s != nil {
		selected := obj("modelId", s.model.ID, "encoderRevision", s.revision)
		if s.model.Dimensions != nil {
			_ = selected.Set("requestedDimensions", float64(*s.model.Dimensions))
		}
		_ = body.Set("selected", selected)
	}
	if state.job != nil {
		_ = body.Set("job", state.job)
	}
	_ = body.Set("coverage", obj("eligible", float64(eligible), "indexed", float64(indexed), "pending", float64(eligible-indexed)))
	_ = body.Set("retained", obj("generations", float64(retainedGenerations), "vectors", float64(retainedVectors)))
	return jsjson.Marshal(body)
}

func indexedMemories(ctx context.Context, conn *sql.Conn, space string) (out map[string]struct{}, err error) {
	rows, err := conn.QueryContext(ctx, "SELECT memory_id FROM memory_vectors WHERE space_id=?", space)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = make(map[string]struct{})
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

type indexStatement struct {
	ctx context.Context
	*sql.Stmt
}

func (s indexStatement) Query(args ...any) (assertions.Rows, error) {
	return s.QueryContext(s.ctx, args...)
}
