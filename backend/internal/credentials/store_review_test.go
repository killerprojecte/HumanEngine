package credentials

import (
	"context"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Run an authority change during encryption, after Apply's initial readiness
// fence but before PrepareWrite. The cached-key path must still fail closed.
type authorityChangeReader struct {
	beforeRead func()
	reader     io.Reader
}

func (r authorityChangeReader) Read(b []byte) (int, error) {
	r.beforeRead()
	return r.reader.Read(b)
}

func TestSecretCachedEncryptionRejectsInterleavedRevocation(t *testing.T) {
	s := secretFixtureStore(t, true)
	ctx := context.Background()
	if _, err := s.Apply(ctx, "ns", "key", &Mutation{Action: "set", Value: "old"}); err != nil {
		t.Fatal(err)
	}
	head, err := s.authority.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gateBefore, err := recovery.ReadGate(ctx, s.main)
	if err != nil {
		t.Fatal(err)
	}
	var revisionBefore string
	if err := s.main.QueryRow("SELECT revision FROM recovery_security_state").Scan(&revisionBefore); err != nil {
		t.Fatal(err)
	}
	changed := false
	s.cipher.random = authorityChangeReader{beforeRead: func() {
		if changed {
			return
		}
		changed = true
		if _, err := s.authority.Append(ctx, recovery.SecurityState{Revision: "12345678-1234-4123-8123-123456789013"}, head, false); err != nil {
			t.Fatal(err)
		}
	}, reader: rand.Reader}
	if _, err := s.Apply(ctx, "ns", "key", &Mutation{Action: "set", Value: "new"}); err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") {
		t.Fatal("cached encryption did not reject stale authority", err)
	}
	if !changed {
		t.Fatal("test did not reach cached encryption")
	}
	got, err := s.authority.Head(ctx)
	if err != nil || got.Sequence != head.Sequence+1 {
		t.Fatal("failed mutation appended an unexpected intent", err)
	}
	gateAfter, err := recovery.ReadGate(ctx, s.main)
	if err != nil || gateAfter != gateBefore {
		t.Fatal("failed mutation changed main ACK", err)
	}
	var revisionAfter, envelope string
	if err := s.main.QueryRow("SELECT revision FROM recovery_security_state").Scan(&revisionAfter); err != nil {
		t.Fatal(err)
	}
	if revisionAfter != revisionBefore {
		t.Fatal("failed mutation changed main security revision")
	}
	if err := s.main.QueryRow("SELECT ciphertext FROM secrets").Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	value, err := s.cipher.Decrypt("ns", "key", envelope)
	if err != nil || value != "old" {
		t.Fatal("failed mutation changed credential", err)
	}
	if _, _, err := s.Get(ctx, "ns", "key"); err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") {
		t.Fatal("unacknowledged authority change allowed credential publication", err)
	}
}
