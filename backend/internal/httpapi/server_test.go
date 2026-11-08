package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const fixtureToken = "synthetic-http-test-token"
const fixtureSecret = "synthetic-presence-only-value-not-for-wire"

func fixtureServer(t *testing.T) (*Server, *sql.DB, *sql.DB, recovery.Gate) {
	t.Helper()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if err := os.Mkdir(authorityPath, 0700); err != nil {
		t.Fatal(err)
	}
	authority, err := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authority.Close() })
	_, err = authority.Exec(`CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if err != nil {
		t.Fatal(err)
	}
	// Use the same independent main/authority layout required by production
	// writers; authority may not be nested inside the main database directory.
	dataPath := filepath.Join(root, "live")
	if err := os.Mkdir(dataPath, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataPath, "main.sqlite")
	if _, err := recovery.AuthorityPath(path, authorityPath); err != nil {
		t.Fatal("invalid fixture authority layout", err)
	}
	main, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { main.Close() })
	_, err = main.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);
CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE secrets(namespace TEXT,name TEXT,value TEXT,PRIMARY KEY(namespace,name));`)
	if err != nil {
		t.Fatal(err)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: recovery.ReaderCapability, State: "ready"}
	raw, err := json.Marshal(gate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = main.Exec("INSERT INTO secrets VALUES('model','provider',?)", fixtureSecret); err != nil {
		t.Fatal(err)
	}
	server, err := Open(context.Background(), path, fixtureToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server, main, authority, gate
}

// Only object member ordering is ignored. Every string/code unit, property name,
// array order, number, boolean and presence distinction is compared losslessly.
func unordered(value any) any {
	switch v := value.(type) {
	case *jsjson.Object:
		out := make(map[string]any, v.Len())
		for _, member := range v.Members() {
			out[member.Key] = unordered(member.Value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = unordered(child)
		}
		return out
	default:
		return v
	}
}

func readResponse(t *testing.T, client *http.Client, base, method, path, token string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Request-ID", "synthetic-request-id")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for key, value := range map[string]string{
		"Content-Type": "application/json", "Cache-Control": "no-store",
		"X-Content-Type-Options": "nosniff", "X-Request-ID": "synthetic-request-id",
	} {
		if response.Header.Get(key) != value {
			t.Fatalf("%s header changed", key)
		}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(body) {
		t.Fatal("response contains non-UTF8 bytes instead of JSON surrogate escapes")
	}
	if bytes.Contains(body, []byte(fixtureSecret)) {
		t.Fatal("secret value escaped presence-only projection")
	}
	return response.StatusCode, body
}

func TestRealTCPStringsMatchNode(t *testing.T) {
	raw, err := os.ReadFile("../contract/testdata/http-strings-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Node     string
		Fixtures []struct {
			Schema, Label, Input, ReadOutput string
			Valid, Wire                      bool
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Node != "v24.22.1" {
		t.Fatal("unexpected Node oracle")
	}
	server, main, _, _ := fixtureServer(t)
	// httptest.NewServer is an actual TCP listener, not a ResponseRecorder.
	tcp := httptest.NewServer(server)
	defer tcp.Close()
	for _, f := range corpus.Fixtures {
		if !f.Wire || f.Schema == "Scene" && !f.Valid {
			continue
		}
		t.Run(f.Schema+"/"+f.Label, func(t *testing.T) {
			key, table := "settings", "config"
			if f.Schema == "ModelConfig" {
				key = "models"
			}
			if f.Schema == "Scene" {
				key, table = "scenes", "scenes"
			}
			if table == "scenes" {
				_, err = main.Exec("INSERT OR REPLACE INTO scenes VALUES('scene',?)", f.Input)
			} else {
				_, err = main.Exec("INSERT OR REPLACE INTO config VALUES(?,?)", key, f.Input)
			}
			if err != nil {
				t.Fatal(err)
			}
			status, body := readResponse(t, tcp.Client(), tcp.URL, "GET", "/api/"+key, fixtureToken)
			if !f.Valid {
				if status != http.StatusServiceUnavailable {
					t.Fatalf("invalid Node input returned %d", status)
				}
				return
			}
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, body)
			}
			actual, err := jsjson.ParseBytes(body)
			if err != nil {
				t.Fatal(err)
			}
			want, err := jsjson.Parse(f.ReadOutput)
			if err != nil {
				t.Fatal(err)
			}
			if table == "scenes" {
				want = []any{want}
			}
			if !reflect.DeepEqual(unordered(actual), unordered(want)) {
				t.Fatal("TCP projection differs from source Node read projection")
			}
			status, body = readResponse(t, tcp.Client(), tcp.URL, "HEAD", "/api/"+key, fixtureToken)
			if status != http.StatusOK || len(body) != 0 {
				t.Fatalf("HEAD response: %d, %q", status, body)
			}
			var persisted string
			if table == "scenes" {
				err = main.QueryRow("SELECT value FROM scenes WHERE id='scene'").Scan(&persisted)
			} else {
				err = main.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&persisted)
			}
			if err != nil || persisted != f.Input {
				t.Fatal("query rewrote persisted source JSON", err)
			}
		})
	}
}

func TestTCPAuthorizationAndPublicationFence(t *testing.T) {
	server, main, authority, gate := fixtureServer(t)
	tcp := httptest.NewServer(server)
	defer tcp.Close()
	for _, test := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/api/settings", "", 401}, {"GET", "/api/settings", "wrong", 401},
		{"PUT", "/api/settings", fixtureToken, 503}, {"POST", "/api/models", fixtureToken, 503},
		{"GET", "/api/unknown", fixtureToken, 503}, {"GET", "/api/settings", fixtureToken, 200},
	} {
		status, _ := readResponse(t, tcp.Client(), tcp.URL, test.method, test.path, test.token)
		if status != test.status {
			t.Fatalf("%s %s returned %d", test.method, test.path, status)
		}
	}
	request, _ := http.NewRequest("GET", tcp.URL+"/api/models", nil)
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	response, err := tcp.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err = hex.DecodeString(response.Header.Get("X-Request-ID")); err != nil || len(response.Header.Get("X-Request-ID")) != 32 {
		t.Fatal("missing generated request ID")
	}
	if _, err = main.Exec("INSERT INTO config VALUES('settings',?)", string(contract.DefaultSettingsInput())); err != nil {
		t.Fatal(err)
	}
	payload := `{"kind":"source","sceneId":"synthetic-scene","messageId":"unacknowledged"}`
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|1|%s|%s", gate.Head.InstanceID, gate.Head.Hash, payload)))
	if _, err = authority.Exec("INSERT INTO revocations VALUES(1,?,?,?)", gate.Head.Hash, payload, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/settings", "/api/models", "/api/scenes"} {
		status, body := readResponse(t, tcp.Client(), tcp.URL, "GET", path, fixtureToken)
		if status != 503 || bytes.Contains(body, []byte("HumanEngine")) {
			t.Fatalf("unacknowledged authority append allowed projection: %d %s", status, body)
		}
	}
}

func TestProjectionEncoderPreservesPropertyIdentity(t *testing.T) {
	high := jsjson.FromUTF16([]uint16{0xd800})
	low := jsjson.FromUTF16([]uint16{0xdc00})
	value := map[string]any{high: low, "\ufffd": high, "__proto__": []any{high, low, "😀"}}
	raw, err := marshalProjection(value)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, unordered(parsed)) {
		t.Fatal("object property/string identity collapsed")
	}
	if _, err = marshalProjection(map[string]any{"bad": string([]byte{0xff})}); err == nil {
		t.Fatal("malformed byte silently replaced")
	}
}
