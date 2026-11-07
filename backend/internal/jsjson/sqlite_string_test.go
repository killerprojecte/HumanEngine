package jsjson

import (
	"database/sql"
	hexcodec "encoding/hex"
	"encoding/json"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNodeUTF8TextBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-strings.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Units  []uint16
			UTF8   string
			RowHex string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, entry := range fixture.Cases {
		value, err := UTF8Text(FromUTF16(entry.Units))
		if err != nil {
			t.Fatal(err)
		}
		if hexcodec.EncodeToString([]byte(value)) != entry.UTF8 {
			t.Fatalf("%x: %x != %s", entry.Units, value, entry.UTF8)
		}
	}
	if _, err = UTF8Text(string([]byte{0xff})); err == nil {
		t.Fatal("noncanonical bytes accepted")
	}
}

func TestNodeSQLiteBindingAndCollisionBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-strings.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Units       []uint16
			RowHex      string
			LookupValue string
			RowCount    int
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("CREATE TABLE keys(id TEXT PRIMARY KEY,value TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i, entry := range fixture.Cases {
		key, err := UTF8Text(FromUTF16(entry.Units))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT OR REPLACE INTO keys VALUES(?,?)", key, strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		var actualHex, value string
		var count int
		if err = db.QueryRow("SELECT hex(id),value FROM keys WHERE id=?", key).Scan(&actualHex, &value); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow("SELECT count(*) FROM keys").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if actualHex != strings.ToUpper(entry.RowHex) || value != entry.LookupValue || count != entry.RowCount {
			t.Fatalf("case%d hex=%s value=%s count=%d", i, actualHex, value, count)
		}
	}
}
