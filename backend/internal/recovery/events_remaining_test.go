package recovery

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// JSON decoding is only for the oracle envelope. Raw event strings and payloads
// are escaped one level deeper and go through jsjson in ValidateEvent/scan.
type authorityDomainFixture struct {
	InstanceID string
	Head       Head
	Cases      []struct {
		Label    string
		Raw      string
		Accepted bool
	}
	Rows []struct {
		Sequence     int64
		PreviousHash string
		Payload      string
		Hash         string
	}
}

func readAuthorityDomainFixture(t *testing.T) authorityDomainFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/authority-domains-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture authorityDomainFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestRemainingAuthorityDomainsMatchActualNode(t *testing.T) {
	fixture := readAuthorityDomainFixture(t)
	if len(fixture.Cases) < 2000 {
		t.Fatal("authority oracle case inventory unexpectedly shrank")
	}
	for _, entry := range fixture.Cases {
		t.Run(entry.Label, func(t *testing.T) {
			if err := ValidateEvent(entry.Raw); (err == nil) != entry.Accepted {
				t.Fatalf("accepted=%t want=%t: %v; raw=%s", err == nil, entry.Accepted, err, entry.Raw)
			}
		})
	}
}

func TestMixedAuthorityDomainHistoryMatchesActualNode(t *testing.T) {
	oracle := readAuthorityDomainFixture(t)
	authority, _, db, _ := fixture(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insert, err := tx.Prepare("INSERT INTO revocations VALUES(?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()
	for _, row := range oracle.Rows {
		if _, err = insert.Exec(row.Sequence, row.PreviousHash, row.Payload, row.Hash); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Audit validates all domains and hashes without replaying or applying any
	// event to the application database. It must preserve Node's original head.
	head, err := authority.Head(context.Background())
	if err != nil || head != oracle.Head {
		t.Fatalf("Node history head mismatch: got=%+v want=%+v err=%v", head, oracle.Head, err)
	}
	if authority.Scans() != 1 {
		t.Fatal("expected one complete history audit")
	}
	reopened, err := Open(context.Background(), authority.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if head, err := reopened.Head(context.Background()); err != nil || head != oracle.Head {
		t.Fatalf("reopened Node history head mismatch: got=%+v want=%+v err=%v", head, oracle.Head, err)
	}
	for _, row := range oracle.Rows {
		var payload string
		if err := db.QueryRow("SELECT payload FROM revocations WHERE sequence=?", row.Sequence).Scan(&payload); err != nil || payload != row.Payload {
			t.Fatalf("history payload changed at %d: %v", row.Sequence, err)
		}
	}
}
