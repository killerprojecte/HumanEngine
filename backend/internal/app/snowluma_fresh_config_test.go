package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestFreshSnowLumaHTTPConfigurationStoresBothCredentials(t *testing.T) {
	ctx := context.Background()
	options := applicationOptions(t)
	options.Entries = []string{}
	a, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})

	db, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='recovery_security_state'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("fresh installation has security state: %d %v", tables, err)
	}
	before, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	const path = "/api/plugins/snowluma/config"
	const actionToken = "synthetic-fresh-snow-action-token"
	const eventToken = "synthetic-fresh-snow-event-token"
	appRequest(t, a, http.MethodPut, path, map[string]any{
		"transport": "http", "url": "http://127.0.0.1:41274", "accountId": "42",
		"eventHost": "127.0.0.1", "eventPort": 41275, "token": actionToken, "eventToken": eventToken,
	})

	config := appRequest(t, a, http.MethodGet, path, nil).(*jsjson.Object)
	list := appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
	found := false
	for _, plugin := range list {
		if field(plugin, "id") != "snowluma" {
			continue
		}
		found = true
		flags := field(plugin, "configuredSecrets")
		if field(flags, "token") != true || field(flags, "eventToken") != true || field(plugin, "enabled") != false {
			t.Fatal("fresh HTTP configuration did not persist both credential flags while disabled")
		}
		break
	}
	if !found {
		t.Fatal("SnowLuma missing from installed plugins")
	}
	wire, err := jsjson.Stringify(config)
	if err != nil || field(config, "transport") != "http" || field(config, "eventPort") != float64(41275) ||
		strings.Contains(wire, actionToken) || strings.Contains(wire, eventToken) {
		t.Fatal("fresh HTTP configuration was not saved as a write-only credential mutation", err)
	}
	after, err := recovery.ReadGate(ctx, db)
	if err != nil || after.Head.Sequence != before.Head.Sequence+2 {
		t.Fatalf("fresh HTTP configuration did not ACK exactly two security intents: %+v %v", after, err)
	}
	var revision string
	if err := db.QueryRow("SELECT revision FROM recovery_security_state WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(options.Installation.AuthorityDirectory, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	var payload string
	if err := ledger.QueryRow("SELECT payload FROM revocations WHERE sequence=?", after.Head.Sequence).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"kind":"security_state","revision":"`+revision+`"}` {
		t.Fatal("database revision does not match the final authority intent")
	}

	// A distinct host origin already owns the actual dispatch ID. Enabling
	// real SnowLuma must fail before connecting, without replacing the route
	// or leaving the failed WASM generation alive.
	claim, err := a.Engine.Runtime.NewAdapterOwner().Claim("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	if err = claim.Publish(&botruntime.Adapter{Status: func(context.Context) (string, error) {
		return "connected", nil
	}}); err != nil {
		t.Fatal(err)
	}
	defer claim.Release()
	defer claim.Detach()
	var collision *pluginmanager.StartupError
	if err := a.Plugins.Enable(ctx, "snowluma", "synthetic-collision"); !errors.As(err, &collision) || collision.Code != "adapter_id_in_use" {
		t.Fatalf("wrong SnowLuma failure for an occupied ID: %v", err)
	}
	status, err := a.Engine.Runtime.AdapterStatuses(ctx)
	if err != nil || len(status) != 1 || status[0] != (botruntime.AdapterStatus{ID: "snowluma", Status: "connected"}) {
		t.Fatalf("live owner changed after denied installation: %v %v", status, err)
	}
	if a.WASM.Loaded("snowluma") || a.WASM.Loaded("snowluma-policy") {
		t.Fatal("failed installation leaked a guest")
	}
}
