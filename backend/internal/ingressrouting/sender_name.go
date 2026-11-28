package ingressrouting

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// RefreshSenderName fills an existing, unnamed sender in this conversation.
// A later inline name wins, and physical peer I/O happens outside either owner.
func RefreshSenderName(ctx context.Context, main *sql.DB, authority *recovery.Authority, reader *recovery.ReadPublisher,
	scene dispatch.SceneRef, sender, platform string, lookup func(context.Context) (string, error)) (bool, error) {
	key := identityKey(scene.ID, sender)
	const query = `SELECT i.value FROM scenes s JOIN config i ON i.key=?
		WHERE s.id=? AND json_extract(s.value,'$.id')=s.id AND json_extract(s.value,'$.adapterId')=?
		AND json_extract(s.value,'$.accountId')=? AND json_extract(s.value,'$.kind')=?
		AND json_extract(s.value,'$.platformSceneId')=?`
	args := []any{key, scene.ID, scene.AdapterID, scene.AccountID, scene.Kind, scene.PlatformSceneID}
	var eligible, named bool
	err := reader.Publish(ctx, func(c *sql.Conn) ([]byte, error) {
		var raw string
		err := c.QueryRowContext(ctx, query, args...).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		current, err := senderNameRow(raw, platform)
		eligible, named = current != nil, current != nil && truthy(get(current, "senderDisplayName"))
		return nil, err
	}, func([]byte) error { return nil })
	if err != nil || !eligible || named {
		return named, err
	}
	name, err := lookup(ctx)
	if err != nil {
		return false, err
	}
	name, present, err := profileidentity.DisplayName(name)
	if err != nil || !present {
		return false, err
	}
	err = authority.WithWriteSession(ctx, main, func(tx *recovery.WriteSession) error {
		if err := tx.AssertReady(); err != nil {
			return err
		}
		rows, err := tx.Query(query, args...)
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
		current, err := senderNameRow(raw, platform)
		if err != nil || current == nil || truthy(get(current, "senderDisplayName")) {
			return err
		}
		next := object("senderDisplayName", name)
		for _, m := range current.Members() {
			if m.Key != "senderDisplayName" {
				set(next, m.Key, m.Value)
			}
		}
		data, err := jsjson.Stringify(next)
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE config SET value=? WHERE key=?", data, key)
		return err
	})
	return err == nil, err
}

func senderNameRow(raw, platform string) (*jsjson.Object, error) {
	value, err := jsjson.Parse(raw)
	current, ok := value.(*jsjson.Object)
	if !ok || get(current, "senderPlatformId") != platform {
		return nil, err
	}
	return current, err
}
