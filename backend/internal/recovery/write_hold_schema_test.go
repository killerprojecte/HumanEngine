package recovery

import (
	"context"
	"strings"
	"testing"
)

func TestWriteRestoredModelHoldSchemaIsScoped(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec("CREATE TABLE recovery_model_hold(id INTEGER,restore_id TEXT,status TEXT)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := f.authority.Write(ctx, f.main, nil, 1, func(tx *MainMutation) error {
		names, err := tx.RestoredModelHoldSchema()
		if err != nil {
			return err
		}
		if strings.Join(names, ",") != "id,restore_id,status" {
			t.Errorf("unexpected columns: %v", names)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{"PRAGMA main.table_xinfo(recovery_model_hold)", "PRAGMA main.table_xinfo(data)", "PRAGMA user_version=42"} {
		if _, err := f.authority.Write(ctx, f.main, nil, 1, func(tx *MainMutation) error {
			if _, err := tx.RestoredModelHoldSchema(); err != nil {
				t.Fatalf("fixed inspection failed: %v", err)
			}
			_, err := tx.Query(stmt)
			return err
		}); err == nil || !strings.Contains(err.Error(), "not authorized") {
			t.Errorf("generic SQL escaped authorizer: %s: %v", stmt, err)
		}
	}
}
