package ingressrouting

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
)

// SQLiteRepository borrows the shared coordinator and main pool. Its owner must
// outlive the router; it does not open another authority or enroll a database.
type SQLiteRepository struct {
	main      *sql.DB
	authority *recovery.Authority
	scenes    *scenestore.Store
}

func NewSQLiteRepository(main *sql.DB, authority *recovery.Authority) (*SQLiteRepository, error) {
	if main == nil || authority == nil {
		return nil, errors.New("ingress_repository_required")
	}
	return &SQLiteRepository{main: main, authority: authority, scenes: &scenestore.Store{Main: main, Authority: authority}}, nil
}
func (r *SQLiteRepository) value(ctx context.Context, query, key string) (value any, exists bool, err error) {
	key, err = jsjson.UTF8Text(key)
	if err != nil {
		return nil, false, err
	}
	err = r.authority.Publish(ctx, r.main, func(conn *sql.Conn) ([]byte, error) {
		var raw string
		e := conn.QueryRowContext(ctx, query, key).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		exists = true
		value, e = jsjson.Parse(raw)
		return nil, e
	}, func([]byte) error { return nil })
	if err != nil {
		return nil, false, err
	}
	return
}
func (r *SQLiteRepository) Scene(ctx context.Context, id string) (any, bool, error) {
	return r.value(ctx, "SELECT value FROM scenes WHERE id=?", id)
}
func (r *SQLiteRepository) SaveScene(ctx context.Context, scene *jsjson.Object) error {
	_, err := r.scenes.Save(ctx, scene)
	return err
}
func (r *SQLiteRepository) Identity(ctx context.Context, scene, sender string) (any, bool, error) {
	return r.value(ctx, "SELECT value FROM config WHERE key=?", identityKey(scene, sender))
}
func identityKey(scene, sender string) string { return "identity:" + scene + ":" + sender }
func (r *SQLiteRepository) SaveIdentity(ctx context.Context, scene, sender string, value *jsjson.Object) error {
	raw, err := jsjson.Stringify(value)
	if err != nil {
		return err
	}
	key, err := jsjson.UTF8Text(identityKey(scene, sender))
	if err != nil {
		return err
	}
	return r.authority.WithWriteSession(ctx, r.main, func(session *recovery.WriteSession) error {
		// Store.set reads/parses the previous value inside its write transaction,
		// even for an ordinary identity key with no security-state side effects.
		rows, e := session.Query("SELECT value FROM config WHERE key=?", key)
		if e != nil {
			return e
		}
		if rows.Next() {
			var previous string
			e = rows.Scan(&previous)
			if e == nil {
				_, e = jsjson.Parse(previous)
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
		_, e = session.Exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, raw)
		return e
	})
}
