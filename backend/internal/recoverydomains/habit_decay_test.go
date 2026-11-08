package recoverydomains

import (
	"context"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// This goes through the real offline maintenance owner and source recall,
// comparing persisted bytes exactly, rather than comparing rounded magnitudes.
func TestOfflineHabitFractionalDecayPersistedBytes(t *testing.T) {
	node := oracle(t, "habit-decay")
	for _, v := range arr(get(node, "cases")) {
		c := obj(v)
		t.Run(str(get(c, "name")), func(t *testing.T) {
			f := setup(t)
			seed(t, f, makeObject("habits", []any{get(c, "initial")}))
			now := time.UnixMilli(int64(num(get(c, "now"))))
			options := Options{Now: func() time.Time { return now }}
			o, e := NewWithOptions(f.owner, f.media, options)
			must(t, e)
			event := stringify(t, get(c, "event"))
			must(t, o.Apply(context.Background(), event))
			assertRaw := func() {
				t.Helper()
				var raw string
				must(t, f.conn.QueryRowContext(context.Background(), "SELECT value FROM habits WHERE id='h'").Scan(&raw))
				if raw != str(get(c, "final")) {
					t.Fatalf("persisted JSON mismatch\n got %s\nwant %s", raw, str(get(c, "final")))
				}
				// Source identity deliberately includes raw weight/confidence. The effective
				// magnitude is omitted; these fixtures have no further expiry or edit.
				record := obj(parse(t, raw))
				stable := record.Clone()
				stable.Delete("weight")
				stable.Delete("confidence")
				if str(get(record, "status")) == "active" {
					identity := digest(stringify(t, makeObject("raw", record, "effective", stable, "policy", get(node, "policy"))))
					if identity != str(get(c, "identity")) {
						t.Fatalf("identity %s != %v", identity, get(c, "identity"))
					}
				} else if get(c, "identity") != nil {
					t.Fatal("inactive source unexpectedly has identity")
				}
			}
			assertRaw()
			must(t, f.owner.Apply(context.Background(), 1, func(tx *recovery.MaintenanceTx) error { return o.Verify(context.Background(), tx, nil) }))
			// Constructing a new owner and repeating recall must not round/change the
			// stored bytes or replay learning. Physical live restart is covered separately.
			o, e = NewWithOptions(f.owner, f.media, options)
			must(t, e)
			assertRaw()
			must(t, o.Apply(context.Background(), event))
			assertRaw()
		})
	}
}
