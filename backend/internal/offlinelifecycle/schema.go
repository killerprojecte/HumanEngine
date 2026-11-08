package offlinelifecycle

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sort"
)

func schemas(ctx context.Context, tx *recovery.MaintenanceTx, pattern string, known map[string][]string, allObjects bool, message string) (int, error) {
	query := "SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name LIKE ?"
	if allObjects {
		query = "SELECT name,type FROM sqlite_master WHERE name LIKE ?"
	}
	rows, e := tx.QueryContext(ctx, query, pattern)
	if e != nil {
		return 0, e
	}
	names := []string{}
	for rows.Next() {
		var name, kind string
		if e = rows.Scan(&name, &kind); e != nil {
			break
		}
		if kind != "table" || known[name] == nil {
			e = errors.New(message)
			break
		}
		names = append(names, name)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return 0, e
	}
	for _, name := range names {
		rows, e = tx.QueryContext(ctx, `PRAGMA table_xinfo("`+name+`")`)
		if e != nil {
			return 0, e
		}
		actual := []string{}
		for rows.Next() {
			var cid, nn, pk, hidden int
			var column, typ string
			var def any
			if e = rows.Scan(&cid, &column, &typ, &nn, &def, &pk, &hidden); e != nil {
				break
			}
			actual = append(actual, column)
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return 0, e
		}
		expected := append([]string{}, known[name]...)
		sort.Strings(actual)
		sort.Strings(expected)
		if len(actual) != len(expected) {
			return 0, errors.New(message)
		}
		for i := range actual {
			if actual[i] != expected[i] {
				return 0, errors.New(message)
			}
		}
	}
	return len(names), nil
}
func recalled(ctx context.Context, tx *recovery.MaintenanceTx, scene, message string) (bool, error) {
	var yes bool
	e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?)", scene, message).Scan(&yes)
	return yes, e
}
func finalEvents(events []string, key func(*object) string) ([]*object, error) {
	latest := map[string]*object{}
	order := []string{}
	for _, raw := range events {
		v, e := event(raw)
		if e != nil {
			return nil, e
		}
		k := key(v)
		if k == "" {
			continue
		}
		if _, ok := latest[k]; !ok {
			order = append(order, k)
		}
		latest[k] = v
	}
	out := []*object{}
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out, nil
}
func terminals(events []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, raw := range events {
		v, e := event(raw)
		if e != nil {
			return nil, e
		}
		if get(v, "kind") == "assertion" && get(v, "terminal") == true {
			out[str(get(v, "id"))] = true
		}
	}
	return out, nil
}
