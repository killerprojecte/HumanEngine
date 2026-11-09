package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

func (a *Application) restorePlugins(ctx context.Context, owners Owners) error {
	for _, id := range a.Plugins.RegisteredIDs() {
		enabled := false
		err := owners.Authority.Publish(ctx, owners.Main, func(c *sql.Conn) ([]byte, error) {
			var encoded string
			err := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", "plugin-enabled:"+id).Scan(&encoded)
			if err == sql.ErrNoRows {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			v, err := jsjson.Parse(encoded)
			if err != nil {
				return nil, err
			}
			enabled = jsTruthy(v)
			return nil, nil
		}, func([]byte) error { return nil })
		if err != nil {
			return err
		}
		if !enabled {
			continue
		}
		if err = a.Plugins.Enable(ctx, id, ""); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Source startup reports one failed plugin and continues with the others.
			emit(a.options.Logs, observability.Event{Level: "error", Module: "plugin", Code: "plugin.enable_failed", Message: "Configured plugin failed to start", Fields: map[string]any{"pluginId": id}})
		}
	}
	return nil
}
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && x == x
	default:
		return true
	}
}

func localEntries(ctx context.Context, owners Owners, options Options) ([]string, error) {
	if options.Entries != nil {
		return append([]string{}, options.Entries...), nil
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if raw, ok := lookup("HUMANENGINE_PLUGINS"); ok {
		out := []string{}
		for _, entry := range strings.Split(raw, ",") {
			if entry = strings.TrimSpace(entry); entry != "" {
				out = append(out, entry)
			}
		}
		return out, nil
	}
	out := []string{}
	err := owners.Authority.Publish(ctx, owners.Main, func(c *sql.Conn) ([]byte, error) {
		var raw string
		err := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key='localPlugins'").Scan(&raw)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		value, err := jsjson.Parse(raw)
		if err != nil {
			return nil, err
		}
		entries, ok := value.([]any)
		if !ok {
			return nil, errors.New("local_plugins_configuration_invalid")
		}
		for _, value := range entries {
			entry, ok := value.(string)
			if !ok {
				return nil, errors.New("local_plugins_configuration_invalid")
			}
			out = append(out, entry)
		}
		return nil, nil
	}, func([]byte) error { return nil })
	return out, err
}
