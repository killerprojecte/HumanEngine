package recovery

import (
	"context"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// The heads below were independently checked against the actual Node 24.22.1
// RecoveryAuthority after inserting this lexically noncanonical but valid row.
// Historical JSON is hashed/deduplicated as stored, never decoded and rewritten.
func TestLosslessHistoryPreservesRawPayloadHashAndExactDedup(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	raw := ` { "messageId":"\uD800", "kind" : "source", "sceneId":"s" } `
	before := Head{InstanceID: f.gate.Head.InstanceID, Sequence: 1, Hash: "0209849850fa63786965bc6dc6dfea242988401c11edf97ab8fe6182c97bfa8a"}
	if _, err := f.external.Exec("INSERT INTO revocations VALUES(?,?,?,?)", before.Sequence, f.gate.Head.Hash, raw, before.Hash); err != nil {
		t.Fatal(err)
	}
	if observed, err := f.authority.Head(ctx); err != nil || observed != before {
		t.Fatalf("raw history head: %+v %v", observed, err)
	}
	event := Source{SceneID: "s", MessageID: jsjson.FromUTF16([]uint16{0xd800})}
	want := Head{InstanceID: before.InstanceID, Sequence: 2, Hash: "119f5b7ef18a2270f1bf9616526ea6857f1b4769b3d6e8add40ca0f37c804491"}
	after, err := f.authority.Append(ctx, event, before, false)
	if err != nil || after != want {
		t.Fatalf("semantic equality must not deduplicate different payload bytes: %+v %v", after, err)
	}
	if repeated, err := f.authority.Append(ctx, event, after, false); err != nil || repeated != after {
		t.Fatalf("identical payload did not deduplicate: %+v %v", repeated, err)
	}
	var retained string
	if err := f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=1").Scan(&retained); err != nil || retained != raw {
		t.Fatalf("historical payload rewritten: %q %v", retained, err)
	}
	// Changing only escape spelling is semantic equality, but still corruption
	// unless the chain hash is updated. A decoded/re-encoded verifier misses it.
	if _, err := f.external.Exec("UPDATE revocations SET payload=? WHERE sequence=1", strings.Replace(raw, "D800", "d800", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.authority.Head(ctx); err == nil || !strings.Contains(err.Error(), "recovery_chain_invalid") {
		t.Fatalf("lexical-only historical tampering accepted: %v", err)
	}
}
