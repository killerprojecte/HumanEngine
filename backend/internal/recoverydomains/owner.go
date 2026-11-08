package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"path/filepath"
	"strings"
	"time"
)

type Owner struct {
	owner           *recovery.Maintenance
	mediaRoot       string
	options         Options
	applied         []*object
	removedPolicies map[string]bool
	expectedMedia   map[string]string
}

func New(owner *recovery.Maintenance, root string) (*Owner, error) {
	return NewWithOptions(owner, root, Options{})
}
func NewWithOptions(owner *recovery.Maintenance, root string, options Options) (*Owner, error) {
	if owner == nil {
		return nil, errors.New("recovery_maintenance_required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	absolute, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	o := &Owner{owner: owner, mediaRoot: absolute, options: options, removedPolicies: map[string]bool{}, expectedMedia: map[string]string{}}
	ctx := context.Background()
	if e = o.AssertSupported(ctx); e != nil {
		return nil, e
	}
	// better-sqlite3 exec commits each constructor DDL statement independently.
	// A later rebuild failure must not remove already-created service schemas.
	for _, ddl := range []string{habitDDL, memeDDL} {
		for _, statement := range strings.Split(ddl, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if e = owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return exec(ctx, tx, statement) }); e != nil {
				return nil, e
			}
		}
	}
	e = owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		assets, e := values(context.Background(), tx, "SELECT value FROM meme_assets ORDER BY rowid DESC")
		if e != nil {
			return e
		}
		for _, a := range assets {
			state := str(get(a, "analysisState"))
			if state == "running" || state == "retrying" {
				set(a, "analysisState", "interrupted")
				a.Delete("analysisNextRetryAt")
			}
			if e = o.indexConcepts(context.Background(), tx, a); e != nil {
				return e
			}
			if str(get(a, "analysisState")) == "interrupted" {
				raw, e := encode(a)
				if e != nil {
					return e
				}
				if e = exec(context.Background(), tx, "UPDATE meme_assets SET value=? WHERE id=?", raw, get(a, "id")); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	e = owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return exec(ctx, tx, experienceDDL) })
	return o, e
}
func (o *Owner) AssertSupported(ctx context.Context) error {
	return o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return assertSchema(ctx, tx) })
}
func (o *Owner) captureMedia(ctx context.Context, tx *recovery.MaintenanceTx, id string) error {
	a, e := read(ctx, tx, "SELECT value FROM meme_assets WHERE id=?", id)
	if e != nil {
		return e
	}
	var v any = a
	if a == nil {
		v = nil
	}
	h, e := hash(v, o.options)
	if e == nil {
		o.expectedMedia[id] = h
	}
	return e
}
func (o *Owner) Apply(ctx context.Context, raw string) error {
	event, e := parseEvent(raw)
	if e != nil {
		return e
	}
	kind := str(get(event, "kind"))
	if kind == "assertion" || kind == "fingerprint" {
		return nil
	}
	if kind == "meme_delete" {
		if e = o.deleteMeme(ctx, event); e != nil {
			return e
		}
		e = o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return o.captureMedia(ctx, tx, str(get(event, "id"))) })
		if e == nil {
			o.applied = append(o.applied, event)
		}
		return e
	}
	// Commit expectation state only with the SQL transaction, including its loss latch.
	removed := map[string]bool{}
	for k, v := range o.removedPolicies {
		removed[k] = v
	}
	media := map[string]string{}
	for k, v := range o.expectedMedia {
		media[k] = v
	}
	e = o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := assertSchema(ctx, tx); e != nil {
			return e
		}
		switch kind {
		case "source":
			return o.recall(ctx, tx, event)
		case "habit_delete":
			return o.deleteHabit(ctx, tx, event)
		case "meme_state":
			if e := o.stateMeme(ctx, tx, event); e != nil {
				return e
			}
			return o.captureMedia(ctx, tx, str(get(event, "id")))
		case "experience_delete":
			return o.deleteExperience(ctx, tx, str(get(event, "sceneId")), str(get(event, "id")))
		case "experience_state":
			return o.stateExperience(ctx, tx, event)
		default:
			return errors.New("recovery_domain_event_invalid")
		}
	})
	if e != nil {
		o.removedPolicies = removed
		o.expectedMedia = media
		return e
	}
	o.applied = append(o.applied, event)
	return nil
}
func (o *Owner) Verify(ctx context.Context, tx *recovery.MaintenanceTx, _ []string) error {
	if e := tx.CheckBinding(ctx, o.owner); e != nil {
		return e
	}
	if e := assertSchema(ctx, tx); e != nil {
		return e
	}
	last := map[string]*object{}
	order := []string{}
	for _, v := range o.applied {
		k := str(get(v, "kind"))
		if k == "source" {
			scene, msg := get(v, "sceneId"), get(v, "messageId")
			for _, table := range []string{"meme_sources", "meme_feedback", "meme_media_references"} {
				found, e := exists(ctx, tx, "SELECT 1 FROM "+table+" WHERE scene_id=? AND message_id=? LIMIT 1", scene, msg)
				if e != nil {
					return e
				}
				if found {
					return errors.New("recovery_domain_source_remaining")
				}
			}
			out, e := values(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE scene=? AND (source=? OR json_extract(value,'$.target.messageId')=?)", scene, msg, msg)
			if e != nil {
				return e
			}
			if len(out) > 0 {
				return errors.New("recovery_domain_source_remaining")
			}
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
				for _, s := range sources {
					if get(obj(s), "sceneId") == scene && get(obj(s), "messageId") == msg {
						return errors.New("recovery_domain_source_remaining")
					}
				}
			}
			continue
		}
		domain := k
		for i, c := range k {
			if c == '_' {
				domain = k[:i]
				break
			}
		}
		id, _ := encode([]any{domain, get(v, "sceneId"), get(v, "id")})
		if last[id] == nil {
			order = append(order, id)
		}
		last[id] = v
	}
	for _, id := range order {
		v := last[id]
		kind := str(get(v, "kind"))
		var table string
		switch kind {
		case "habit_delete":
			table = "habits"
		case "meme_delete":
			table = "meme_assets"
		case "experience_delete":
			table = "memory_experience_policies"
		}
		if table != "" {
			found, e := exists(ctx, tx, "SELECT 1 FROM "+table+" WHERE id=?", get(v, "id"))
			if e != nil {
				return e
			}
			if found {
				return errors.New("recovery_domain_delete_incomplete")
			}
		}
		if kind == "experience_state" && !o.removedPolicies[key(str(get(v, "sceneId")), str(get(v, "id")))] {
			p, e := read(ctx, tx, "SELECT value FROM memory_experience_policies WHERE id=?", get(v, "id"))
			if e != nil {
				return e
			}
			if p == nil || get(p, "version") != get(v, "version") || get(p, "status") != get(v, "status") {
				return errors.New("recovery_domain_state_incomplete")
			}
		}
		if kind == "meme_state" {
			a, e := read(ctx, tx, "SELECT value FROM meme_assets WHERE id=?", get(v, "id"))
			if e != nil {
				return e
			}
			var material any = a
			if a == nil {
				material = nil
			}
			h, e := hash(material, o.options)
			if e != nil {
				return e
			}
			if h != o.expectedMedia[str(get(v, "id"))] {
				return errors.New("recovery_domain_state_incomplete")
			}
		}
	}
	return nil
}
