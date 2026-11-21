package app

import (
	"context"
	"database/sql"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/proactiveowner"
)

func (a *Application) startProactive() error {
	owner, err := proactiveowner.New(proactiveowner.Config{
		Scenes: a.proactiveScenes,
		Proactive: func(sceneID string, scheduled bool) (<-chan struct{}, error) {
			handle, err := a.Engine.Runtime.Proactive(sceneID, scheduled)
			if handle == nil {
				return nil, err
			}
			return handle.Done, err
		},
		OnError: func(error) {
			emit(a.options.Logs, observability.Event{Level: "error", Module: "runtime", Code: "proactive.failed", Message: "Scheduled proactive opportunity failed"})
		},
		Clock: a.options.proactiveClock,
	})
	if err != nil {
		return err
	}
	a.proactive = owner
	return owner.Start()
}

func (a *Application) proactiveScenes() ([]proactiveowner.Scene, error) {
	ctx := context.Background()
	var scenes []proactiveowner.Scene
	err := a.readPublisher.Publish(ctx, func(conn *sql.Conn) ([]byte, error) {
		rows, err := conn.QueryContext(ctx, "SELECT value FROM scenes ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				return nil, err
			}
			decoded, err := jsjson.Parse(raw)
			if err != nil {
				return nil, err
			}
			row, _ := decoded.(*jsjson.Object)
			if row == nil {
				continue
			}
			id, _ := row.Get("id")
			kind, _ := row.Get("kind")
			enabled, _ := row.Get("proactiveEnabled")
			sceneID, _ := id.(string)
			sceneKind, _ := kind.(string)
			scenes = append(scenes, proactiveowner.Scene{ID: sceneID, Kind: sceneKind, ProactiveEnabled: enabled == true})
		}
		return nil, rows.Err()
	}, func([]byte) error { return nil })
	return scenes, err
}
