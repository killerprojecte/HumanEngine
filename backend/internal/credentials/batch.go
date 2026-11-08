package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Change is one source-ordered native credential mutation. Values never enter
// recovery intents or logs. The caller must not mutate inputs during admission.
type Change struct {
	Namespace, Name string
	Mutation        *Mutation
}

type preparedChange struct{ namespace, name, action, ciphertext string }

// ApplyWith commits source-ordered secret mutations and the owner's SQL write
// in one main transaction. The callback must only use the supplied mutation;
// recursively entering recovery or this Store would violate transaction ownership.
// Encryption may create a key before a later failure, exactly as ordinary Apply.
func (s *Store) ApplyWith(ctx context.Context, changes []Change, apply func(*recovery.MainMutation) error) (recovery.Head, error) {
	snapshot := make([]Change, len(changes))
	for n, change := range changes {
		snapshot[n] = change
		if change.Mutation != nil {
			v := *change.Mutation
			snapshot[n].Mutation = &v
		}
	}
	if err := s.lock(ctx); err != nil {
		return recovery.Head{}, err
	}
	defer s.unlock()
	if err := s.readyRead(ctx, func(*sql.Conn) error { return nil }); err != nil {
		return recovery.Head{}, err
	}
	prepared := make([]preparedChange, 0, len(snapshot))
	for _, change := range snapshot {
		mutation := change.Mutation
		if mutation == nil || mutation.Action == "preserve" {
			continue
		}
		ns, name, err := keys(change.Namespace, change.Name)
		if err != nil {
			return recovery.Head{}, err
		}
		entry := preparedChange{namespace: ns, name: name, action: mutation.Action}
		if mutation.Action != "clear" {
			if mutation.Action != "set" {
				return recovery.Head{}, ErrInvalidSecret
			}
			value, err := validValue(mutation.Value)
			if err != nil {
				return recovery.Head{}, err
			}
			if s.cipher == nil {
				if err = s.readyRead(ctx, func(c *sql.Conn) error {
					var has bool
					if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets)").Scan(&has); err != nil {
						return err
					}
					return s.loadCipher(has)
				}); err != nil {
					return recovery.Head{}, err
				}
			}
			entry.ciphertext, err = s.cipher.Encrypt(change.Namespace, change.Name, value)
			if err != nil {
				return recovery.Head{}, err
			}
		}
		prepared = append(prepared, entry)
	}
	return s.authority.PrepareWrite(ctx, s.main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		type operation struct {
			preparedChange
			revision string
		}
		ops := []operation{}
		intents := []recovery.Revocation{}
		type key struct{ namespace, name string }
		exists := map[key]bool{}
		known := map[key]bool{}
		for _, entry := range prepared {
			k := key{entry.namespace, entry.name}
			if entry.action == "clear" && !known[k] {
				rows, err := read.Query("SELECT 1 FROM secrets WHERE namespace=? AND name=?", entry.namespace, entry.name)
				if err != nil {
					return recovery.WritePlan{}, err
				}
				exists[k] = rows.Next()
				known[k] = true
				if err = errors.Join(rows.Err(), rows.Close()); err != nil {
					return recovery.WritePlan{}, err
				}
			}
			if entry.action == "clear" && !exists[k] {
				continue
			}
			if len(ops) == 0 {
				if err := requireSecretSecuritySchema(read); err != nil {
					return recovery.WritePlan{}, err
				}
			}
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return recovery.WritePlan{}, err
			}
			b[6] = b[6]&15 | 64
			b[8] = b[8]&63 | 128
			revision := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
			ops = append(ops, operation{entry, revision})
			intents = append(intents, recovery.SecurityState{Revision: revision})
			exists[k] = entry.action == "set"
			known[k] = true
		}
		return recovery.WritePlan{MinimumReader: 1, Intents: intents, Apply: func(tx *recovery.MainMutation) error {
			for _, op := range ops {
				if _, err := tx.Exec("INSERT INTO main.recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", op.revision); err != nil {
					return err
				}
				var err error
				if op.action == "clear" {
					_, err = tx.Exec("DELETE FROM main.secrets WHERE namespace=? AND name=?", op.namespace, op.name)
				} else {
					_, err = tx.Exec("INSERT INTO main.secrets(namespace,name,ciphertext) VALUES(?,?,?) ON CONFLICT(namespace,name) DO UPDATE SET ciphertext=excluded.ciphertext", op.namespace, op.name, op.ciphertext)
				}
				if err != nil {
					return err
				}
			}
			if apply != nil {
				return apply(tx)
			}
			return nil
		}}, nil
	})
}
