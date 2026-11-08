package pluginmanager

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (m *Manager) readConfig(ctx context.Context, id string) (*jsjson.Object, error) {
	var value *jsjson.Object
	err := m.authority.Publish(ctx, m.main, func(c *sql.Conn) ([]byte, error) {
		var raw string
		err := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", "plugin:"+id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return []byte(`{}`), nil
		}
		return []byte(raw), err
	}, func(raw []byte) error {
		v, err := jsjson.Parse(string(raw))
		if err != nil {
			return err
		}
		var ok bool
		value, ok = v.(*jsjson.Object)
		if !ok {
			return errors.New("invalid_plugin_configuration")
		}
		return nil
	})
	return value, err
}

func (m *Manager) writePreparedConfig(ctx context.Context, id string, prepare func() (any, error)) error {
	_, err := m.authority.Write(ctx, m.main, nil, 1, func(tx *recovery.MainMutation) error {
		value, err := prepare()
		if err != nil {
			return err
		}
		raw, err := jsjson.Stringify(value)
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO main.config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "plugin:"+id, raw)
		return err
	})
	return err
}
func (m *Manager) writeEnabled(ctx context.Context, id string, value bool) error {
	raw := "false"
	if value {
		raw = "true"
	}
	_, err := m.authority.Write(ctx, m.main, nil, 1, func(tx *recovery.MainMutation) error {
		_, err := tx.Exec("INSERT INTO main.config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "plugin-enabled:"+id, raw)
		return err
	})
	return err
}
