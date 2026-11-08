package learningowner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func searchIndexOracle(t *testing.T) *Object {
	t.Helper()
	raw, e := os.ReadFile("testdata/search-index.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsjson.ParseBytes(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*Object)
}
func jsonEqual(t *testing.T, a, b any) {
	t.Helper()
	x, e := jsjson.Stringify(a)
	if e != nil {
		t.Fatal(e)
	}
	y, e := jsjson.Stringify(b)
	if e != nil {
		t.Fatal(e)
	}
	var av, bv any
	_ = json.Unmarshal([]byte(x), &av)
	_ = json.Unmarshal([]byte(y), &bv)
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("got %s\nwant %s", x, y)
	}
}
func TestSearchIndexActualSourceHashes(t *testing.T) {
	for _, m := range indexObject(get(searchIndexOracle(t), "sources")).Members() {
		raw, e := os.ReadFile(filepath.Join("../../..", m.Key))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != m.Value {
			t.Fatalf("source drift %s", m.Key)
		}
	}
}
func TestSearchActualSourceTermsAndRanking(t *testing.T) {
	requireLexicalICU(t)
	root := searchIndexOracle(t)
	for _, v := range arr(get(root, "lexical")) {
		c := indexObject(v)
		got, e := memorylexical.Terms(str(c, "query"))
		if e != nil {
			t.Fatal(e)
		}
		values := make([]any, len(got))
		for i, s := range got {
			values[i] = s
		}
		jsonEqual(t, values, get(c, "terms"))
	}
	for _, v := range arr(get(root, "search")) {
		c := indexObject(v)
		t.Run(str(c, "query"), func(t *testing.T) {
			memory, f := serviceSetup(t)
			for _, v := range arr(get(c, "records")) {
				r := indexObject(v)
				raw, _ := jsjson.Stringify(r)
				if _, e := f.main.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", str(r, "id"), "scene:s", nil, str(r, "content"), str(r, "id"), raw); e != nil {
					t.Fatal(e)
				}
			}
			got, e := memory.Search(context.Background(), memoryservice.Context{SceneID: "s"}, str(c, "query"), "", nil)
			if e != nil {
				t.Fatal(e)
			}
			values := make([]any, len(got))
			for i, r := range got {
				values[i] = r
			}
			jsonEqual(t, values, get(c, "expected"))
		})
	}
}
func TestPublicCandidateActualNonUnicodeCaseFold(t *testing.T) {
	root := searchIndexOracle(t)
	for _, v := range arr(get(root, "publicCandidates")) {
		c := indexObject(v)
		if got := publicTechnicalCandidate(str(c, "content"), str(c, "domain")); got != get(c, "expected") {
			t.Fatalf("public candidate %s", str(c, "content"))
		}
	}
}
func indexCount(t *testing.T, f scopedFixture) int {
	t.Helper()
	var n int
	if e := f.main.QueryRow("SELECT count(*) FROM memory_vectors").Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func checkIndexIntegrity(t *testing.T, f scopedFixture) {
	t.Helper()
	var s string
	if e := f.main.QueryRow("PRAGMA integrity_check").Scan(&s); e != nil || s != "ok" {
		t.Fatalf("integrity %s %v", s, e)
	}
	rows, e := f.main.Query("PRAGMA foreign_key_check")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
}
func TestIndexActualSourceBootstrapAndRecovery(t *testing.T) {
	for _, v := range arr(get(searchIndexOracle(t), "index")) {
		c := indexObject(v)
		name := str(c, "scenario")
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var o *Owner
			var id string
			o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if name == "changed-content" {
					if _, e := o.d.Memory.Update(context.Background(), id, obj("content", "Changed synthetic parser.")); e != nil {
						t.Error(e)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			})
			record, e := o.d.Memory.Create(context.Background(), obj("scope", obj("kind", "scene", "sceneId", "s"), "content", "Synthetic parser.", "kind", "fact"))
			if e != nil {
				t.Fatal(e)
			}
			id = str(record, "id")
			if name == "legacy" {
				if e = o.initializeIndex(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if name == "unconfigured" {
				if _, e = f.main.Exec("UPDATE config SET value='{}' WHERE key='models'"); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			o.indexMemory(ctx, id, str(record, "content"))
			state, e := o.indexState(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if calls.Load() != int32(get(c, "calls").(float64)) || (state.active != nil) != get(c, "active") || indexCount(t, f) != int(get(c, "vectors").(float64)) {
				t.Fatalf("calls=%d active=%v vectors=%d", calls.Load(), state.active, indexCount(t, f))
			}
			checkIndexIntegrity(t, f)
			// A newly constructed index never replays or adopts legacy vectors. Existing
			// committed generation and vector bytes survive a fresh authority verification.
			before := calls.Load()
			if e = o.initializeIndex(context.Background()); e != nil {
				t.Fatal(e)
			}
			if calls.Load() != before || o.indexBootstrap {
				t.Fatal("restart replay/bootstrap")
			}
			if e = f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error { return ws.AssertReady() }); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func sourceProposal(t *testing.T, o *Owner, f scopedFixture) (*Object, *Object) {
	t.Helper()
	message := obj("id", "m", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "parts", []any{obj("type", "text", "text", "HTTP defines request methods.")}, "sentAt", "2026-01-01T00:00:00.000Z", "receivedAt", "2026-01-01T00:00:00.000Z")
	hash, e := sourceevents.RuntimeSourceBodyHash(message)
	if e != nil {
		t.Fatal(e)
	}
	observed, e := f.events.Observe(context.Background(), sourceevents.Change{Kind: "ingress", Identity: sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: "account", MessageID: "m", SenderID: "p"}, BodyHash: hash})
	if e != nil || observed.Ref == nil {
		t.Fatalf("observe %v %v", observed, e)
	}
	_ = message.Set("sourceEventRef", observed.Ref.Value())
	evidence, e := sourceadmission.TrustedEvidence([]*Object{message}, func(string, string) (bool, error) { return false, nil })
	if e != nil {
		t.Fatal(e)
	}
	candidate := obj("content", "HTTP defines request methods.", "kind", "fact", "sources", []any{obj("sceneId", "s", "messageId", "m", "senderId", "p")})
	result, e := o.d.Memory.Propose(context.Background(), obj("sceneId", "s", "simulation", false, "provenance", "source-only", "sourceEvidence", evidence), candidate, memoryservice.ProposalOptions{})
	if e != nil || result.Status != "saved" {
		t.Fatalf("propose %s %s %v", result.Status, result.Reason, e)
	}
	return result.Memory, message
}
func TestAfterSavedActualPublicReviewAndIndex(t *testing.T) {
	for _, v := range arr(get(searchIndexOracle(t), "publicReview")) {
		oracle := indexObject(v)
		shareable := get(oracle, "shareable") == true
		t.Run(fmt.Sprint(shareable), func(t *testing.T) {
			var requests []map[string]any
			var requestsMu sync.Mutex
			var calls atomic.Int32
			o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request map[string]any
				if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
					t.Error(e)
				}
				requestsMu.Lock()
				requests = append(requests, request)
				requestsMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "embeddings") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
					return
				}
				args, _ := json.Marshal(map[string]any{"shareable": shareable, "publicnessConfidence": .99, "sourceSpecific": false, "containsPersonalData": false})
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"content": "", "tool_calls": []any{map[string]any{"id": "review", "type": "function", "function": map[string]any{"name": "knowledge__review", "arguments": string(args)}}}}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
			})
			record, message := sourceProposal(t, o, f)
			saved, e := o.AfterSaved(context.Background(), "s", obj("publicTechnicalDomain", "http"), record, []*Object{message})
			if e != nil {
				t.Fatal(e)
			}
			if (str(indexObject(get(saved, "scope")), "kind") == "sharedPublicKnowledge") != shareable || calls.Load() != 2 || indexCount(t, f) != 1 {
				t.Fatalf("after saved %v calls=%d vectors=%d", saved, calls.Load(), indexCount(t, f))
			}
			requestsMu.Lock()
			defer requestsMu.Unlock()
			expected := indexObject(get(oracle, "request"))
			messages := requests[0]["messages"].([]any)
			wantMessages := arr(get(expected, "messages"))
			if messages[0].(map[string]any)["content"] != get(indexObject(wantMessages[0]), "content") {
				t.Fatal("public prompt differs")
			}
			payloadRaw := messages[1].(map[string]any)["content"].(string)
			payload, e := jsjson.Parse(payloadRaw)
			if e != nil {
				t.Fatal(e)
			}
			payload.(*Object).Delete("now")
			jsonEqual(t, payload, get(indexObject(wantMessages[1]), "content"))
			tools := requests[0]["tools"].([]any)
			schema := tools[0].(map[string]any)["function"].(map[string]any)["parameters"]
			raw, _ := json.Marshal(schema)
			actual, _ := jsjson.Parse(string(raw))
			jsonEqual(t, actual, get(indexObject(arr(get(expected, "tools"))[0]), "inputSchema"))
			checkIndexIntegrity(t, f)
		})
	}
}
func TestIndexRevocationDuringProviderDoesNotWrite(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Error("release timed out")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	record, e := o.d.Memory.Create(context.Background(), obj("scope", obj("kind", "scene", "sceneId", "s"), "content", "Synthetic parser.", "kind", "fact"))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.indexMemory(context.Background(), str(record, "id"), str(record, "content"))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("provider not entered")
	}
	if _, e = f.main.Exec("UPDATE scenes SET value=json_set(value,'$.memoryReadRevision',1)"); e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("index did not return")
	}
	if indexCount(t, f) != 0 {
		t.Fatal("revoked read wrote vector")
	}
}
func TestVectorSearchActualFloat32Source(t *testing.T) {
	requireLexicalICU(t)
	for _, v := range arr(get(searchIndexOracle(t), "vectors")) {
		c := indexObject(v)
		t.Run(fmt.Sprint(get(indexObject(get(c, "spec")), "normalized")), func(t *testing.T) {
			memory, f := serviceSetup(t)
			spec := indexObject(get(c, "spec"))
			raw, _ := jsjson.Stringify(spec)
			if _, e := f.main.Exec("INSERT INTO vector_spaces VALUES('space',?)", raw); e != nil {
				t.Fatal(e)
			}
			for _, v := range arr(get(c, "records")) {
				record := indexObject(v).Clone()
				vector := []float64{}
				for _, n := range arr(get(record, "vector")) {
					vector = append(vector, n.(float64))
				}
				record.Delete("vector")
				raw, _ := jsjson.Stringify(record)
				if _, e := f.main.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", str(record, "id"), "scene:s", nil, str(record, "content"), str(record, "id"), raw); e != nil {
					t.Fatal(e)
				}
				if e := f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error {
					return memory.IndexInSession(context.Background(), ws, str(record, "id"), "space", vector)
				}); e != nil {
					t.Fatal(e)
				}
			}
			for _, v := range arr(get(c, "bytes")) {
				r := indexObject(v)
				var bytes string
				if e := f.main.QueryRow("SELECT hex(embedding) FROM memory_vectors WHERE memory_id=?", str(r, "memory_id")).Scan(&bytes); e != nil {
					t.Fatal(e)
				}
				if bytes != str(r, "bytes") {
					t.Fatalf("vector bytes %s want %s", bytes, str(r, "bytes"))
				}
			}
			query := []float64{}
			for _, n := range arr(get(c, "queryVector")) {
				query = append(query, n.(float64))
			}
			got, e := memory.Search(context.Background(), memoryservice.Context{SceneID: "s"}, "nonlexical query", "", &memoryservice.SearchVector{SpaceID: "space", Vector: query})
			if e != nil {
				t.Fatal(e)
			}
			rows := make([]any, len(got))
			for i, r := range got {
				rows[i] = r
			}
			jsonEqual(t, rows, get(c, "expected"))
		})
	}
}

// Reopen the actual on-disk writer pair, not an in-memory approximation. The
// restarted index checks metadata only and must not issue another paid request.
func TestIndexDurableWriterReopenNoReplay(t *testing.T) {
	var calls atomic.Int32
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	record, _ := sourceProposal(t, o, f)
	var e error
	if e = o.indexMemory(context.Background(), str(record, "id"), str(record, "content")); e != nil {
		t.Fatal(e)
	}
	if indexCount(t, f) != 1 {
		t.Fatal("initial vector absent")
	}
	var seq int
	var name, path string
	if e = f.main.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	var gateRaw string
	if e = f.main.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&gateRaw); e != nil {
		t.Fatal(e)
	}
	var gate recovery.Gate
	if e = json.Unmarshal([]byte(gateRaw), &gate); e != nil {
		t.Fatal(e)
	}
	if _, e = recovery.AuthorityPath(path, gate.Authority); e != nil {
		t.Fatal(e)
	}
	if e = f.recovery.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.main.Close(); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if e != nil {
		t.Fatal(e)
	}
	defer main.Close()
	main.SetMaxOpenConns(1)
	authority, e := recovery.OpenWriter(context.Background(), gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer authority.Close()
	restarted := &Owner{d: Dependencies{Main: main, Authority: authority, Now: time.Now}}
	if e = restarted.initializeIndex(context.Background()); e != nil {
		t.Fatal(e)
	}
	if restarted.indexBootstrap || calls.Load() != 1 {
		t.Fatal("restart replayed learning/index")
	}
	var count int
	if e = main.QueryRow("SELECT count(*) FROM memory_vectors").Scan(&count); e != nil || count != 1 {
		t.Fatalf("recovered vector %d %v", count, e)
	}
	for _, table := range []string{"source_event_heads", "memories"} {
		if e = main.QueryRow("SELECT count(*) FROM " + table).Scan(&count); e != nil || count != 1 {
			t.Fatalf("recovered %s %d %v", table, count, e)
		}
	}
	checkIndexIntegrity(t, scopedFixture{main: main, recovery: authority})
	if e = authority.WithWriteSession(context.Background(), main, func(ws *recovery.WriteSession) error { return ws.AssertReady() }); e != nil {
		t.Fatal(e)
	}
}

func TestEncoderIdentityMatchesActualWHATWGSource(t *testing.T) {
	for _, v := range arr(get(searchIndexOracle(t), "encoders")) {
		c := indexObject(v)
		endpoint := str(c, "endpoint")
		config := obj("providers", []any{obj("id", "p", "protocol", "openai-chat", "baseUrl", endpoint)}, "models", []any{obj("id", "m", "providerId", "p", "apiModelName", "synthetic", "capabilities", []any{"embedding"})}, "routes", []any{obj("role", "embedding", "orderedModelIds", []any{"m"})})
		selected, e := selectEncoder(config)
		want := str(c, "revision")
		if want == "" {
			if e == nil && selected != nil {
				t.Fatalf("accepted source-invalid endpoint %q", endpoint)
			}
		} else if e != nil || selected == nil || selected.revision != want {
			t.Fatalf("encoder %q: %+v %v expected %s", endpoint, selected, e, want)
		}
	}
}

func requireLexicalICU(t *testing.T) {
	t.Helper()
	_, e := memorylexical.Words("synthetic probe")
	if errors.Is(e, nodeicu.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("pinned native ICU source segmentation required")
	}
	if e != nil {
		t.Fatal(e)
	}
}
