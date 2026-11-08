package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

var ErrInvalidSecret = errors.New("invalid_secret")
var ErrSecuritySchemaUnsupported = recovery.ErrSecuritySchemaUnsupported

// Mutation accepts only the source JSON-data mutation boundary. A nil pointer
// means omitted/null; Value must be a canonical jsjson string for action set.
type Mutation struct {
	Action string
	Value  any
}

// Store is server-only. Never serialize its return values or log credentials.
// One installation owns one Store; cached key custody matches SecretStore.
type Store struct {
	main      *sql.DB
	authority *recovery.Authority
	keyPath   string
	mu        chan struct{}
	cipher    *Cipher
}

// NewStore is a constructor/bootstrap operation, not a restricted mutation.
// Like TS it creates secrets but does not enroll, reconcile, or create security
// state. Call only during separately authorized application initialization.
func NewStore(ctx context.Context, main *sql.DB, authority *recovery.Authority, keyPath string) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("secret_store_invalid")
	}
	_, err := main.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS secrets (namespace TEXT NOT NULL, name TEXT NOT NULL, ciphertext TEXT NOT NULL, PRIMARY KEY(namespace,name))")
	if err != nil {
		return nil, err
	}
	return &Store{main: main, authority: authority, keyPath: keyPath, mu: make(chan struct{}, 1)}, nil
}
func (s *Store) readyRead(ctx context.Context, read func(*sql.Conn) error) error {
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) { return nil, read(c) }, func([]byte) error { return nil })
}
func keys(namespace, name string) (string, string, error) {
	ns, err := jsjson.UTF8Text(namespace)
	if err != nil {
		return "", "", err
	}
	n, err := jsjson.UTF8Text(name)
	return ns, n, err
}
func (s *Store) loadCipher(has bool) error {
	if s.cipher != nil {
		return nil
	}
	var key []byte
	var err error
	if s.keyPath == "" {
		key = make([]byte, 32)
		_, err = rand.Read(key)
	} else {
		key, err = LoadKey(s.keyPath, has)
	}
	if err != nil {
		return err
	}
	s.cipher, err = NewCipher(key)
	return err
}
func (s *Store) Has(ctx context.Context, namespace, name string) (bool, error) {
	if err := s.lock(ctx); err != nil {
		return false, err
	}
	defer s.unlock()
	ns, n, err := keys(namespace, name)
	if err != nil {
		return false, err
	}
	var found bool
	err = s.readyRead(ctx, func(c *sql.Conn) error {
		return c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets WHERE namespace=? AND name=?)", ns, n).Scan(&found)
	})
	return found && err == nil, err
}
func (s *Store) Get(ctx context.Context, namespace, name string) (string, bool, error) {
	if err := s.lock(ctx); err != nil {
		return "", false, err
	}
	defer s.unlock()
	return s.get(ctx, namespace, name)
}
func (s *Store) get(ctx context.Context, namespace, name string) (string, bool, error) {
	ns, n, err := keys(namespace, name)
	if err != nil {
		return "", false, err
	}
	var value string
	found := false
	err = s.readyRead(ctx, func(c *sql.Conn) error {
		var envelope string
		err := c.QueryRowContext(ctx, "SELECT ciphertext FROM secrets WHERE namespace=? AND name=?", ns, n).Scan(&envelope)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = s.loadCipher(true); err != nil {
			return err
		}
		value, err = s.cipher.Decrypt(namespace, name, envelope)
		found = err == nil
		return err
	})
	if err != nil {
		return "", false, err
	}
	return value, found, nil
}

// RedactionValues contains plaintext for diagnostic scrubbing only. In
// particular lossy SQLite key aliases must use the stored key as source does.
func (s *Store) RedactionValues(ctx context.Context) ([]string, error) {
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.unlock()
	names := [][2]string{}
	err := s.readyRead(ctx, func(c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, "SELECT namespace,name FROM secrets")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pair [2]string
			if err = rows.Scan(&pair[0], &pair[1]); err != nil {
				return err
			}
			names = append(names, pair)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	values := []string{}
	for _, pair := range names {
		v, found, err := s.get(ctx, pair[0], pair[1])
		if err != nil {
			return nil, err
		}
		if found && v != "" {
			values = append(values, v)
		}
	}
	return values, nil
}
func validValue(v any) (string, error) {
	value, ok := v.(string)
	if !ok {
		return "", ErrInvalidSecret
	}
	units, err := jsjson.UTF16(value)
	if err != nil {
		return "", ErrInvalidSecret
	}
	if len(units) > 16384 {
		return "", ErrInvalidSecret
	}
	for _, u := range units {
		if !jsSpace(u) {
			return value, nil
		}
	}
	return "", ErrInvalidSecret
}
func jsSpace(u uint16) bool {
	switch u {
	case 9, 10, 11, 12, 13, 32, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return u >= 0x2000 && u <= 0x200a
}

// Apply never retries. Failure after intent can require external reconciliation.
func (s *Store) Apply(ctx context.Context, namespace, name string, mutation *Mutation) (recovery.Head, error) {
	// Callers must not race mutation input while this initial snapshot is taken.
	if mutation != nil {
		snapshot := *mutation
		mutation = &snapshot
	}
	if err := s.lock(ctx); err != nil {
		return recovery.Head{}, err
	}
	defer s.unlock()
	err := s.readyRead(ctx, func(*sql.Conn) error { return nil })
	if err != nil {
		return recovery.Head{}, err
	}
	if mutation == nil || mutation.Action == "preserve" {
		return recovery.Head{}, nil
	}
	ns, n, err := keys(namespace, name)
	if err != nil {
		return recovery.Head{}, err
	}
	envelope := ""
	if mutation.Action == "clear" {
		var exists bool
		if err := s.readyRead(ctx, func(c *sql.Conn) error {
			return c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets WHERE namespace=? AND name=?)", ns, n).Scan(&exists)
		}); err != nil {
			return recovery.Head{}, err
		}
		if !exists {
			return recovery.Head{}, nil
		}
	}
	if mutation.Action != "clear" {
		if mutation.Action != "set" {
			return recovery.Head{}, ErrInvalidSecret
		}
		value, err := validValue(mutation.Value)
		if err != nil {
			return recovery.Head{}, err
		}
		// Cached key custody is protected by Store admission. Only first load
		// needs a second current-data fence; do not reserve authority again on
		// every hot set merely to discover that the key is already cached.
		if s.cipher == nil {
			if err = s.readyRead(ctx, func(c *sql.Conn) error {
				var hasAny bool
				if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets)").Scan(&hasAny); err != nil {
					return err
				}
				return s.loadCipher(hasAny)
			}); err != nil {
				return recovery.Head{}, err
			}
		}
		envelope, err = s.cipher.Encrypt(namespace, name, value)
		if err != nil {
			return recovery.Head{}, err
		}
	}
	return s.authority.PrepareWrite(ctx, s.main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		noop := recovery.WritePlan{MinimumReader: 1, Apply: func(*recovery.MainMutation) error { return nil }}
		if mutation.Action == "clear" {
			rows, err := read.Query("SELECT 1 FROM secrets WHERE namespace=? AND name=?", ns, n)
			if err != nil {
				return recovery.WritePlan{}, err
			}
			exists := rows.Next()
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return recovery.WritePlan{}, err
			}
			if !exists {
				return noop, nil
			}
		}
		if err := requireSecretSecuritySchema(read); err != nil {
			return recovery.WritePlan{}, err
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return recovery.WritePlan{}, err
		}
		b[6] = b[6]&15 | 64
		b[8] = b[8]&63 | 128
		revision := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		return recovery.WritePlan{MinimumReader: 1, Intents: []recovery.Revocation{recovery.SecurityState{Revision: revision}}, Apply: func(tx *recovery.MainMutation) error {
			if _, err := tx.Exec("INSERT INTO main.recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", revision); err != nil {
				return err
			}
			if mutation.Action == "clear" {
				_, err := tx.Exec("DELETE FROM main.secrets WHERE namespace=? AND name=?", ns, n)
				return err
			}
			_, err := tx.Exec("INSERT INTO main.secrets(namespace,name,ciphertext) VALUES(?,?,?) ON CONFLICT(namespace,name) DO UPDATE SET ciphertext=excluded.ciphertext", ns, n, envelope)
			return err
		}}, nil
	})
}
func requireSecretSecuritySchema(read *recovery.MainRead) error {
	rows, err := read.Query("SELECT type,sql FROM main.sqlite_master WHERE name='recovery_security_state'")
	if err != nil {
		return err
	}
	var kind, definition string
	exists := rows.Next()
	if exists {
		err = rows.Scan(&kind, &definition)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	normalized := strings.Replace(strings.ToLower(strings.Join(strings.Fields(definition), "")), "ifnotexists", "", 1)
	if !exists || kind != "table" || normalized != "createtablerecovery_security_state(idintegerprimarykeycheck(id=1),revisiontextnotnull)" {
		return ErrSecuritySchemaUnsupported
	}
	return nil
}

func (s *Store) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.mu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Store) unlock() { <-s.mu }
