package credentials

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestCredentialBatchConfigTransaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "commit"
		if fail {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			s := secretFixtureStore(t, true)
			changes := []Change{{"plugin:test", "first", &Mutation{Action: "set", Value: "first-private"}}, {"plugin:test", "second", &Mutation{Action: "set", Value: "second-private"}}, {"plugin:test", "first", &Mutation{Action: "clear"}}}
			expected := errors.New("configuration_write_failed")
			head, err := s.ApplyWith(context.Background(), changes, func(tx *recovery.MainMutation) error {
				if _, err := tx.Exec("INSERT INTO main.config VALUES('plugin:test','{}')"); err != nil {
					return err
				}
				if fail {
					return expected
				}
				return nil
			})
			var secrets, configs int
			if e := s.main.QueryRow("SELECT count(*) FROM secrets").Scan(&secrets); e != nil {
				t.Fatal(e)
			}
			if e := s.main.QueryRow("SELECT count(*) FROM config WHERE key='plugin:test'").Scan(&configs); e != nil {
				t.Fatal(e)
			}
			if fail {
				if !errors.Is(err, expected) || secrets != 0 || configs != 0 {
					t.Fatal(err, secrets, configs)
				}
				var revision string
				if e := s.main.QueryRow("SELECT revision FROM recovery_security_state").Scan(&revision); e != nil || revision != initialSecurity {
					t.Fatal(revision, e)
				}
				if _, e := s.Has(context.Background(), "plugin:test", "second"); e == nil {
					t.Fatal("failed intent gap silently repaired")
				}
			} else {
				if err != nil || secrets != 1 || configs != 1 || head.Sequence != 3 {
					t.Fatal(head, err, secrets, configs)
				}
				value, ok, e := s.Get(context.Background(), "plugin:test", "second")
				if e != nil || !ok || value != "second-private" {
					t.Fatal(ok, e)
				}
			}
		})
	}
}
