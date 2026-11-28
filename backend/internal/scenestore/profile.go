package scenestore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// RefreshGroupName replaces only a numeric discovery placeholder. It never
// creates a scene or lets a late action replace its current name or switches.
func (s *Store) RefreshGroupName(ctx context.Context, reader *recovery.ReadPublisher, scene dispatch.SceneRef, lookup func(context.Context) (string, error)) (bool, error) {
	var missing bool
	err := reader.Publish(ctx, func(c *sql.Conn) ([]byte, error) {
		var raw string
		err := c.QueryRowContext(ctx, "SELECT value FROM scenes WHERE id=?", scene.ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		current, err := profilePlaceholder(scene, raw)
		missing = current != nil
		return nil, err
	}, func([]byte) error { return nil })
	if err != nil || !missing {
		return !missing, err
	}
	name, err := lookup(ctx)
	if err != nil {
		return false, err
	}
	name, present, err := profileidentity.DisplayName(name)
	if err != nil || !present || name == scene.PlatformSceneID {
		return false, err
	}
	err = s.Authority.WithWriteSession(ctx, s.Main, func(tx *recovery.WriteSession) error {
		if err := tx.AssertReady(); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT value FROM scenes WHERE id=?", scene.ID)
		if err != nil {
			return err
		}
		var raw string
		found := rows.Next()
		if found {
			err = rows.Scan(&raw)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil || !found {
			return err
		}
		current, err := profilePlaceholder(scene, raw)
		if err != nil || current == nil {
			return err
		}
		_ = current.Set("name", name)
		data, err := jsjson.Stringify(current)
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE scenes SET value=? WHERE id=?", data, scene.ID)
		return err
	})
	return err == nil, err
}

func profilePlaceholder(scene dispatch.SceneRef, raw string) (*jsjson.Object, error) {
	parsed, err := jsjson.Parse(raw)
	if err != nil {
		return nil, err
	}
	current, ok := parsed.(*jsjson.Object)
	if !ok || value(current, "id") != scene.ID || value(current, "adapterId") != scene.AdapterID ||
		value(current, "accountId") != scene.AccountID || value(current, "kind") != "group" ||
		value(current, "platformSceneId") != scene.PlatformSceneID || value(current, "name") != scene.PlatformSceneID {
		return nil, nil
	}
	return current, nil
}
