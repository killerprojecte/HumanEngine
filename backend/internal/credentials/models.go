package credentials

// Model configuration and its credentials share a single main transaction.
// Calling Apply repeatedly is not equivalent: source Store.saveModels rolls
// back every credential change when its final configuration write fails.
import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (s *Store) OwnedBy(main *sql.DB, authority *recovery.Authority) bool {
	return s != nil && s.main == main && s.authority == authority
}

// SaveModels accepts the already schema-validated configuration and ordered
// write-only credential object. It revalidates configuration at the Store
// boundary, as the source does, and never persists plaintext or response flags.
func (s *Store) SaveModels(ctx context.Context, input *jsjson.Object, mutations *jsjson.Object) (recovery.Head, error) {
	schemas, err := contract.SharedSchemas()
	if err != nil {
		return recovery.Head{}, err
	}
	raw, err := jsjson.Marshal(input)
	if err != nil {
		return recovery.Head{}, err
	}
	config, err := schemas.ParseOrdered("ModelConfig", raw)
	if err != nil {
		return recovery.Head{}, err
	}
	providers := modelField(config, "providers").([]any)
	present := map[string]bool{}
	for _, v := range providers {
		p := v.(*jsjson.Object)
		present[modelField(p, "id").(string)] = true
		p.Delete("credentialConfigured")
	}
	normalizeModelRoutes(config)
	if mutations == nil {
		mutations = new(jsjson.Object)
	}
	// Snapshot nested mutation values before admission; callers must not mutate
	// their input during this initial snapshot.
	mr, err := jsjson.Marshal(mutations)
	if err != nil {
		return recovery.Head{}, err
	}
	mv, err := jsjson.ParseBytes(mr)
	if err != nil {
		return recovery.Head{}, err
	}
	mutations = mv.(*jsjson.Object)
	for _, m := range mutations.Members() {
		if !present[m.Key] {
			return recovery.Head{}, errors.New("unknown_credential_provider")
		}
	}
	encoded, err := jsjson.Stringify(config)
	if err != nil {
		return recovery.Head{}, err
	}
	if err = s.lock(ctx); err != nil {
		return recovery.Head{}, err
	}
	defer s.unlock()
	return s.authority.PrepareOrderedWrite(ctx, s.main, func(read *recovery.MainRead) (recovery.OrderedWritePlan, error) {
		previous := `{"providers":[],"models":[],"routes":[]}`
		rows, e := read.Query("SELECT value FROM main.config WHERE key='models'")
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		if rows.Next() {
			e = rows.Scan(&previous)
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		before, e := schemas.ParseOrdered("ModelConfig", []byte(previous))
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		ordered := []jsjson.Member{}
		for _, v := range modelField(before, "providers").([]any) {
			id := modelField(v.(*jsjson.Object), "id").(string)
			if !present[id] {
				m, _ := jsjson.NewObject(jsjson.Member{Key: "action", Value: "clear"})
				ordered = append(ordered, jsjson.Member{Key: id, Value: m})
			}
		}
		ordered = append(ordered, mutations.Members()...)
		remaining := map[string]bool{}
		rows, e = read.Query("SELECT namespace,name FROM main.secrets")
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		for rows.Next() {
			var ns, name string
			if e = rows.Scan(&ns, &name); e != nil {
				break
			}
			remaining[ns+"\x00"+name] = true
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		plan := recovery.OrderedWritePlan{MinimumReader: 1}
		revision := func() (string, error) {
			var b [16]byte
			if _, e := rand.Read(b[:]); e != nil {
				return "", e
			}
			b[6] = b[6]&15 | 64
			b[8] = b[8]&63 | 128
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
		}
		setRevision := func(tx *recovery.MainMutation, v string) error {
			_, e := tx.Exec("INSERT INTO main.recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", v)
			return e
		}
		for _, m := range ordered {
			mutation, ok := m.Value.(*jsjson.Object)
			if !ok {
				return recovery.OrderedWritePlan{}, ErrInvalidSecret
			}
			action, _ := modelField(mutation, "action").(string)
			if action == "preserve" {
				continue
			}
			clear := action == "clear"
			if clear && !remaining["model\x00"+m.Key] {
				continue
			}
			has := len(remaining) > 0
			name := m.Key
			value := modelField(mutation, "value")
			envelope := ""
			rev, e := revision()
			if e != nil {
				return recovery.OrderedWritePlan{}, e
			}
			step := recovery.OrderedWriteStep{EnsureSecurityState: true, Intents: []recovery.Revocation{recovery.SecurityState{Revision: rev}}, BeforeIntent: func() error {
				if !clear {
					if action != "set" {
						return ErrInvalidSecret
					}
					text, e := validValue(value)
					if e != nil {
						return e
					}
					if s.cipher == nil {
						if e = s.loadCipher(has); e != nil {
							return e
						}
					}
					envelope, e = s.cipher.Encrypt("model", name, text)
					if e != nil {
						return e
					}
				}
				return nil
			}, Apply: func(tx *recovery.MainMutation) error {
				if e := setRevision(tx, rev); e != nil {
					return e
				}
				if clear {
					_, e := tx.Exec("DELETE FROM main.secrets WHERE namespace='model' AND name=?", name)
					return e
				}
				_, e := tx.Exec("INSERT INTO main.secrets(namespace,name,ciphertext) VALUES('model',?,?) ON CONFLICT(namespace,name) DO UPDATE SET ciphertext=excluded.ciphertext", name, envelope)
				return e
			}}
			plan.Steps = append(plan.Steps, step)
			if clear {
				delete(remaining, "model\x00"+name)
			} else {
				remaining["model\x00"+name] = true
			}
		}
		beforeSecurity, e := modelSecurity(before)
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		afterSecurity, e := modelSecurity(config)
		if e != nil {
			return recovery.OrderedWritePlan{}, e
		}
		last := recovery.OrderedWriteStep{Apply: func(tx *recovery.MainMutation) error {
			_, e := tx.Exec("INSERT INTO main.config(key,value) VALUES('models',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encoded)
			return e
		}}
		if beforeSecurity != afterSecurity {
			rev, e := revision()
			if e != nil {
				return recovery.OrderedWritePlan{}, e
			}
			last.EnsureSecurityState = true
			last.Intents = []recovery.Revocation{recovery.SecurityState{Revision: rev}}
			write := last.Apply
			last.Apply = func(tx *recovery.MainMutation) error {
				if e := setRevision(tx, rev); e != nil {
					return e
				}
				return write(tx)
			}
		}
		plan.Steps = append(plan.Steps, last)
		return plan, nil
	})
}

func modelField(o *jsjson.Object, k string) any { v, _ := o.Get(k); return v }
func normalizeModelRoutes(c *jsjson.Object) {
	for _, v := range modelField(c, "routes").([]any) {
		r := v.(*jsjson.Object)
		if modelField(r, "role") == "thinker" {
			_ = r.Set("role", "replyer")
		}
	}
}
func modelSecurity(c *jsjson.Object) (string, error) {
	out := new(jsjson.Object)
	for _, part := range []struct {
		name   string
		fields []string
	}{{"providers", []string{"id", "protocol", "baseUrl", "credentialEnv"}}, {"models", []string{"id", "providerId", "apiModelName", "capabilities"}}, {"routes", []string{"role", "orderedModelIds"}}} {
		values := []any{}
		for _, v := range modelField(c, part.name).([]any) {
			p := v.(*jsjson.Object)
			o := new(jsjson.Object)
			for _, k := range part.fields {
				value, ok := p.Get(k)
				if ok {
					if k == "role" && value == "thinker" {
						value = "replyer"
					}
					_ = o.Set(k, value)
				}
			}
			values = append(values, o)
		}
		_ = out.Set(part.name, values)
	}
	return jsjson.Stringify(out)
}
