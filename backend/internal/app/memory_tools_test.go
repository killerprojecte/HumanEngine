package app

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryLearningEnabledPropagatesActualReadFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "data", "main.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := recovery.OpenInstallationGate(ctx, db, path, filepath.Join(root, "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);INSERT INTO scenes VALUES('s','{"id":"s","learningEnabled":true}')`); err != nil {
		t.Fatal(err)
	}
	store, err := assertions.New(db, authority, assertions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := memorySceneLearningEnabled(ctx, store, "s")
	if err != nil || !enabled {
		t.Fatalf("actual enabled read: %v %v", enabled, err)
	}
	// A concrete SQL read error must reach the caller. It is not a disabled scene.
	if _, err = db.Exec(`DROP TABLE scenes`); err != nil {
		t.Fatal(err)
	}
	enabled, err = memorySceneLearningEnabled(ctx, store, "s")
	if err == nil || enabled {
		t.Fatalf("actual missing-table failure suppressed: %v %v", enabled, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	enabled, err = memorySceneLearningEnabled(ctx, store, "s")
	if err == nil || enabled {
		t.Fatalf("closed-owner failure suppressed: %v %v", enabled, err)
	}
}

func TestMemoryToolArrayKeepsReceiptIdentityAndSerializes(t *testing.T) {
	record := memoryToolObject("id", "original", "content", "synthetic")
	values := memoryToolArray([]*jsjson.Object{record})
	if len(values) != 1 || values[0] != record {
		t.Fatal("original receipt pointer lost")
	}
	encoded, err := jsjson.Stringify(memoryToolObject("value", values))
	if err != nil || encoded != "{\"value\":[{\"id\":\"original\",\"content\":\"synthetic\"}]}" {
		t.Fatalf("receipt serialization: %s %v", encoded, err)
	}
	if values := memoryToolArray(nil); values == nil || len(values) != 0 {
		t.Fatal("empty source array became null")
	}
}

func TestMemoryAndIdentityActualReceiptSerialization(t *testing.T) {
	budget, err := replyplan.NewEvidenceBudget(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.person", "memory.identities"} {
		t.Run(name, func(t *testing.T) {
			original := memoryToolObject("id", "synthetic-original")
			typed := []*jsjson.Object{original}
			_, err := budget.Receipt(nil, memoryToolObject("id", "tool:1", "toolName", name, "value", typed), "call", nil)
			if !errors.Is(err, jsjson.ErrUnsupportedType) {
				t.Fatalf("old typed slice did not reproduce JSON failure: %v", err)
			}
			delivered, err := budget.Receipt(nil, memoryToolObject("id", "tool:1", "toolName", name, "value", memoryToolArray(typed)), "call", nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := delivered.Receipt.Get("value")
			values, ok := raw.([]any)
			if !ok || len(values) != 1 || values[0] != original {
				t.Fatal("actual planner budget lost original receipt pointer")
			}
			if _, err = jsjson.Stringify(delivered.Message); err != nil {
				t.Fatal(err)
			}
		})
	}
}
