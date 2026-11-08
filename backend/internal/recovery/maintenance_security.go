package recovery

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

func exactColumns(ctx context.Context, tx *MaintenanceTx, table string, expected []string, sorted bool, message string) error {
	rows, e := tx.QueryContext(ctx, `PRAGMA table_xinfo("`+table+`")`)
	if e != nil {
		return e
	}
	columns := []string{}
	for rows.Next() {
		var cid, nn, pk, hidden int
		var name, typ string
		var def any
		if e = rows.Scan(&cid, &name, &typ, &nn, &def, &pk, &hidden); e != nil {
			break
		}
		columns = append(columns, name)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return e
	}
	if sorted {
		sort.Strings(columns)
		expected = append([]string{}, expected...)
		sort.Strings(expected)
	}
	if len(columns) != len(expected) {
		return errors.New(message)
	}
	for i := range columns {
		if columns[i] != expected[i] {
			return errors.New(message)
		}
	}
	return nil
}

// AssertSecurityState requires exact latest trusted credential revision. It
// neither reconstructs credentials nor hashes secret values into authority.
func (tx *MaintenanceTx) AssertSecurityState(ctx context.Context, entries []RecoveryEntry) error {
	if e := tx.CheckScope(ctx); e != nil {
		return e
	}
	var latest any
	for _, entry := range entries {
		if e := ValidateEvent(entry.Payload); e != nil {
			return e
		}
		value, _ := jsjson.Parse(entry.Payload)
		event := value.(*jsjson.Object)
		kind, _ := event.Get("kind")
		if kind == "security_state" {
			latest, _ = event.Get("revision")
		}
	}
	rows, e := tx.QueryContext(ctx, "SELECT type FROM sqlite_master WHERE name='recovery_security_state'")
	if e != nil {
		return e
	}
	exists := rows.Next()
	var kind string
	if exists {
		e = rows.Scan(&kind)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return e
	}
	var revision any
	if exists {
		if kind != "table" {
			return errors.New("recovery_security_state_invalid")
		}
		if e = exactColumns(ctx, tx, "recovery_security_state", []string{"id", "revision"}, false, "recovery_security_state_invalid"); e != nil {
			return e
		}
		rows, e = tx.QueryContext(ctx, "SELECT id,revision FROM recovery_security_state LIMIT 2")
		if e != nil {
			return e
		}
		count := 0
		for rows.Next() {
			var id any
			if e = rows.Scan(&id, &revision); e != nil {
				break
			}
			count++
			r, ok := revision.(string)
			if count > 1 || id != int64(1) || !ok || !uuid4Pattern.MatchString(r) {
				e = errors.New("recovery_security_state_invalid")
				break
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
	}
	if revision != latest {
		return errors.New("recovery_security_state_unrecoverable")
	}
	return nil
}
