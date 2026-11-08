package recoverydomains

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"math"
)

func (o *Owner) recalculate(h *object) error {
	if num(get(h, "evidenceVersion")) != 2 {
		set(h, "weight", float64(0))
		set(h, "confidence", float64(0))
		if str(get(h, "status")) != "invalidated" {
			set(h, "status", "disputed")
		}
		return nil
	}
	now := float64(o.options.Now().UnixMilli())
	sources := []any{}
	net := 0.0
	input, e := requiredArray(h, "sources")
	if e != nil {
		return e
	}
	for _, v := range input {
		if obj(v) == nil {
			return errors.New("recovery_domain_material_invalid")
		}
		s := obj(v)
		stamp := get(s, "timestamp")
		if stamp != nil && now-num(stamp) < 90*86400000 {
			sources = append(sources, v)
			net += num(get(s, "direction")) * math.Pow(2, -math.Max(0, now-num(stamp))/(30*86400000))
		}
	}
	set(h, "sources", sources)
	set(h, "confidence", math.Min(.75, math.Abs(net)/float64(len(sources)+2)))
	set(h, "weight", math.Copysign(math.Min(.3, math.Abs(net)*.05), net))
	if str(get(h, "status")) != "invalidated" {
		status := "active"
		if math.Abs(net) < 1e-9 || str(get(h, "kind")) == "sceneRule" && net < 0 {
			status = "disputed"
		}
		set(h, "status", status)
	}
	return nil
}
func (o *Owner) deleteHabit(ctx context.Context, tx *recovery.MaintenanceTx, v *object) error {
	var fp string
	e := tx.QueryRowContext(ctx, "SELECT fingerprint FROM habits WHERE id=?", get(v, "id")).Scan(&fp)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil {
		if fp != str(get(v, "fingerprint")) {
			return errors.New("recovery_habit_identity_mismatch")
		}
		if e = tx.RecordReplayEffect(ctx, recovery.HabitDelete{ID: str(get(v, "id")), Fingerprint: fp}); e != nil {
			return e
		}
	}
	if e = exec(ctx, tx, "INSERT OR IGNORE INTO habit_deleted SELECT fingerprint FROM habits WHERE id=?", get(v, "id")); e != nil {
		return e
	}
	if e = exec(ctx, tx, "DELETE FROM habits WHERE id=?", get(v, "id")); e != nil {
		return e
	}
	return exec(ctx, tx, "INSERT OR IGNORE INTO habit_deleted VALUES(?)", get(v, "fingerprint"))
}
func (o *Owner) recallHabits(ctx context.Context, tx *recovery.MaintenanceTx, scene, msg string) error {
	habits, e := values(ctx, tx, "SELECT value FROM habits ORDER BY rowid")
	if e != nil {
		return e
	}
	for _, h := range habits {
		if e = o.recalculate(h); e != nil {
			return e
		}
		sources, e := requiredArray(h, "sources")
		if e != nil {
			return e
		}
		remaining := []any{}
		for _, v := range sources {
			s := obj(v)
			if str(get(s, "sceneId")) != scene || str(get(s, "messageId")) != msg {
				remaining = append(remaining, v)
			}
		}
		if len(remaining) == len(sources) {
			continue
		}
		set(h, "sources", remaining)
		if e = o.recalculate(h); e != nil {
			return e
		}
		if len(remaining) == 0 {
			set(h, "status", "invalidated")
		}
		raw, e := encode(h)
		if e != nil {
			return e
		}
		if e = exec(ctx, tx, "UPDATE habits SET value=? WHERE id=?", raw, get(h, "id")); e != nil {
			return e
		}
	}
	return nil
}
