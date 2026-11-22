package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledApplicationSyntheticMixedLoad(t *testing.T) {
	// Deadlines bound fixture failures; latency observations are not assertions.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(release) }) }
	var decisions, plans, sends atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := serveMixedLoadModel(w, r, entered, release, &decisions, &plans, &sends); err != nil {
			t.Error(err)
			http.Error(w, "invalid synthetic model request", http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	defer unblock()
	o := applicationOptions(t)
	a, err := Open(ctx, o)
	if err != nil {
		if a != nil {
			_ = a.Shutdown(ctx)
		}
		t.Fatal(err)
	}
	defer func() {
		unblock()
		if err := a.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(a)
	defer server.Close()
	requestAt := func(target *httptest.Server, method, path string, body any) (json.RawMessage, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, target.URL+path, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		sourceFixtureAuth(req, true)
		req.Header.Set("Content-Type", "application/json")
		resp, err := target.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		value, err := io.ReadAll(resp.Body)
		if err == nil && (resp.StatusCode != 200 || !json.Valid(value)) {
			err = fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
		}
		return value, err
	}
	request := func(method, path string, body any) (json.RawMessage, error) {
		return requestAt(server, method, path, body)
	}
	call := func(method, path string, body any) json.RawMessage {
		t.Helper()
		out, err := request(method, path, body)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var settings map[string]any
	if err := json.Unmarshal(call("GET", "/api/settings", nil), &settings); err != nil {
		t.Fatal(err)
	}
	settings["sourceEventRevisionsEnabled"] = true
	settings["promptContextWindowEnabled"] = false
	settings["debounceMs"] = 0
	settings["groupTurnCooldownMs"] = 0
	call("PUT", "/api/settings", settings)
	models := fmt.Sprintf(`{"providers":[{"id":"loopback","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":30000}],"models":[{"id":"m","providerId":"loopback","displayName":"Fixture","apiModelName":"mixed-load","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":30000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":30000}]}`, provider.URL+"/v1")
	call("PUT", "/api/models", json.RawMessage(models))
	plugin := "/api/plugins/runtime-fixture/"
	call("PUT", plugin+"config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	call("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	inspect := func() (scene map[string]any, source string, sent []json.RawMessage) {
		t.Helper()
		var v struct {
			Scene  map[string]any
			Source string
			Sent   []json.RawMessage
		}
		if err := json.Unmarshal(call("POST", plugin+"actions/inspect", map[string]any{}), &v); err != nil {
			t.Fatal(err)
		}
		return v.Scene, v.Source, v.Sent
	}
	scene, seed, _ := inspect()
	sceneID, ok := scene["id"].(string)
	if !ok || seed == "" {
		t.Fatal("missing live adapter identity")
	}
	scenePath := "/api/scenes/" + url.PathEscape(sceneID)
	var rows []map[string]any
	if err := json.Unmarshal(call("GET", "/api/scenes", nil), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r["id"] == sceneID {
			scene = r
			break
		}
	}
	scene["participationEnabled"] = true
	scene["learningEnabled"] = false
	scene["name"] = strings.Repeat("synthetic-scene-", 512)
	call("PUT", scenePath, scene)
	expectedScenes := call("GET", "/api/scenes", nil)
	if len(expectedScenes) <= 4096 {
		t.Fatal("scene response must exceed net/http output buffering")
	}

	// Pause the first socket write on a dedicated listener for one request,
	// below the application and net/http response buffering.
	slow := httptest.NewUnstartedServer(a)
	paused := &mixedLoadPausedListener{Listener: slow.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	slow.Listener = paused
	slow.Start()
	defer slow.Close()
	releaseTCP := sync.OnceFunc(func() { close(paused.release) })
	defer releaseTCP()
	type response struct {
		body json.RawMessage
		err  error
	}
	slowDone := make(chan response, 1)
	go func() {
		body, err := requestAt(slow, "GET", "/api/scenes", nil)
		slowDone <- response{body, err}
	}()
	select {
	case <-paused.entered:
	case result := <-slowDone:
		t.Fatalf("response bypassed TCP write barrier: %v", result.err)
	case <-ctx.Done():
		t.Fatal("scene publication did not reach TCP write", ctx.Err())
	}
	call("POST", plugin+"actions/new-source", map[string]any{})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("live ingress did not enter the planner provider", ctx.Err())
	}
	_, source, _ := inspect()
	if source == seed || source == "" {
		t.Fatal("missing fresh source ID")
	}
	settings["groupTurnCooldownMs"] = 1
	call("PUT", "/api/settings", settings)
	var observed struct{ GroupTurnCooldownMs int }
	if err := json.Unmarshal(call("GET", "/api/settings", nil), &observed); err != nil || observed.GroupTurnCooldownMs != 1 {
		t.Fatalf("concurrent settings write not visible: %d %v", observed.GroupTurnCooldownMs, err)
	}
	heldView, err := a.Engine.Runtime.Snapshot(sceneID)
	if err != nil || heldView.Active != 1 {
		t.Fatalf("no active installed turn during held provider: %d %v", heldView.Active, err)
	}
	// The provider cannot respond until these 16 real TCP reads complete.
	paths := []string{scenePath + "/history?source=local", "/api/usage"}
	samples := [2][]time.Duration{}
	type observation struct {
		lane    int
		elapsed time.Duration
		err     error
	}
	done := make(chan observation, 16)
	start := make(chan struct{})
	for worker := range 4 {
		go func() {
			<-start
			for i := range 4 {
				lane := (worker + i) % len(paths)
				now := time.Now()
				out, err := request("GET", paths[lane], nil)
				if err == nil && lane == 0 {
					var history struct{ Items []struct{ ID string } }
					err = json.Unmarshal(out, &history)
					if err == nil && !slices.ContainsFunc(history.Items, func(v struct{ ID string }) bool { return v.ID == source }) {
						err = fmt.Errorf("local history lost in-flight ingress")
					}
				}
				done <- observation{lane, time.Since(now), err}
			}
		}()
	}
	close(start)
	for range 16 {
		v := <-done
		if v.err != nil {
			t.Error(v.err)
		}
		samples[v.lane] = append(samples[v.lane], v.elapsed)
	}
	if plans.Load() != 0 || sends.Load() != 0 {
		t.Fatal("held planner was not isolated")
	}
	// The fixture adapter acknowledges its first send as message "sent-1".
	receiptID := fmt.Sprintf("%s:%x", sceneID, sha256.Sum256([]byte("sent-1")))
	turnStart := time.Now()
	unblock()
	for {
		view, err := a.Engine.Runtime.Snapshot(sceneID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Active == 0 && view.Pending == 0 && view.Waiting == 0 && slices.ContainsFunc(view.Messages, func(row *jsjson.Object) bool { return field(row, "senderId") == "bot" && field(row, "id") == receiptID }) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("turn did not reach retained send and idle state")
		case <-time.After(time.Millisecond):
		}
	}
	turnFinish := time.Since(turnStart)
	_, _, dispatched := inspect()
	if len(dispatched) != 1 || decisions.Load() != 1 || plans.Load() != 1 || sends.Load() != 1 {
		t.Fatalf("unexpected physical calls or sends: decide=%d plan=%d send=%d wire sends=%d", decisions.Load(), plans.Load(), sends.Load(), len(dispatched))
	}
	select {
	case result := <-slowDone:
		t.Fatalf("scene response completed before TCP release: %v", result.err)
	default:
	}
	releaseTCP()
	select {
	case result := <-slowDone:
		if result.err != nil || !bytes.Equal(result.body, expectedScenes) {
			t.Fatalf("accepted scene response changed while paused: bytes=%d want=%d error=%v", len(result.body), len(expectedScenes), result.err)
		}
	case <-ctx.Done():
		t.Fatal("accepted scene response did not drain", ctx.Err())
	}

	// 100 Continue gives a deterministic admitted HTTP/body-reader witness.
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	settings["debugModelIO"] = true
	wire, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "PUT /api/settings HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer synthetic-token\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", len(wire))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	readStatus := func(want int) {
		t.Helper()
		resp, e := http.ReadResponse(reader, &http.Request{Method: "PUT"})
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("expected %d, got %d", want, resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	readStatus(100)
	drained := make(chan error, 1)
	go func() { drained <- a.Shutdown(ctx) }()
	for {
		_, e := request("GET", "/api/settings", nil)
		if e != nil {
			if !strings.HasSuffix(e.Error(), "status 503") {
				t.Fatal(e)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("HTTP did not seal")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-drained:
		t.Fatalf("closed before accepted body completed: %v", err)
	default:
	}
	if _, err = conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	readStatus(200)
	if err = <-drained; err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", o.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var heads, completed int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM source_event_heads WHERE scene_id=? AND message_id IN (?,?)", sceneID, seed, source).Scan(&heads); err != nil || heads != 2 {
		t.Fatalf("durable source heads=%d: %v", heads, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE status='completed' AND role IN ('planner','replyer')").Scan(&completed); err != nil || completed != 3 {
		t.Fatalf("durable completed model attempts=%d: %v", completed, err)
	}
	var flag bool
	var cooldown int
	if err = db.QueryRowContext(ctx, `SELECT json_extract(value,'$.debugModelIO'), json_extract(value,'$.groupTurnCooldownMs') FROM config WHERE key='settings'`).Scan(&flag, &cooldown); err != nil || !flag || cooldown != 1 {
		t.Fatalf("settings did not persist across drain: debug=%v cooldown=%d err=%v", flag, cooldown, err)
	}
	for i, times := range samples {
		slices.Sort(times)
		t.Logf("4 concurrent TCP clients while participation provider held, %s: n=%d median=%s range=%s..%s", paths[i], len(times), (times[(len(times)-1)/2]+times[len(times)/2])/2, times[0], times[len(times)-1])
	}
	t.Logf("one live turn: fixture release to retained send + idle = %s; provider calls=%d; dispatches=1; durable heads=%d", turnFinish, completed, heads)
}

type mixedLoadPausedListener struct {
	net.Listener
	entered, release chan struct{}
}

func (l *mixedLoadPausedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &mixedLoadPausedConn{Conn: c, beforeWrite: sync.OnceFunc(func() { close(l.entered); <-l.release })}, nil
}

type mixedLoadPausedConn struct {
	net.Conn
	beforeWrite func()
}

func (c *mixedLoadPausedConn) Write(b []byte) (int, error) {
	c.beforeWrite()
	return c.Conn.Write(b)
}

func serveMixedLoadModel(w http.ResponseWriter, r *http.Request, entered chan<- struct{}, release <-chan struct{}, decisions, plans, sends *atomic.Int32) error {
	return serveMixedLoadModelTo(w, r, entered, release, decisions, plans, sends, "")
}

func serveMixedLoadModelTo(w http.ResponseWriter, r *http.Request, entered chan<- struct{}, release <-chan struct{}, decisions, plans, sends *atomic.Int32, targetText string) error {
	var req struct {
		Model    string
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "mixed-load" {
		return fmt.Errorf("unexpected fixture provider wire")
	}
	data := map[string]json.RawMessage{}
	hasTool := false
	for _, m := range req.Messages {
		if m.Role == "tool" {
			hasTool = true
		}
		if m.Role == "user" {
			_ = json.Unmarshal([]byte(m.Content), &data)
		}
	}
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	var name string
	var args any
	switch {
	case slices.Contains(names, "participation__decide"):
		name = "participation__decide"
		if decisions.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return r.Context().Err()
		}
		var rows []struct {
			ID    string
			Parts []struct{ Text string }
		}
		if json.Unmarshal(data["messages"], &rows) != nil || len(rows) == 0 {
			return fmt.Errorf("missing participation targets")
		}
		selected := rows[0].ID
		if targetText != "" {
			selected = ""
			for _, row := range rows {
				if slices.ContainsFunc(row.Parts, func(part struct{ Text string }) bool { return part.Text == targetText }) {
					selected = row.ID
					break
				}
			}
			if selected == "" {
				return fmt.Errorf("missing expected participation target")
			}
		}
		args = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{selected}}
	case slices.Contains(names, "reply__plan"):
		name = "reply__plan"
		plans.Add(1)
		var contract map[string]json.RawMessage
		if json.Unmarshal(data["planContract"], &contract) != nil {
			return fmt.Errorf("missing plan contract")
		}
		args = map[string]any{"action": "reply", "deliveryTool": "messages.send", "targetMessageIds": contract["targetMessageIds"], "contextMessageIds": contract["contextMessageIds"], "strategy": "Answer the synthetic request.", "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "actions": []any{}}
	case slices.Contains(names, "messages__send") && !hasTool:
		name = "messages__send"
		sends.Add(1)
		args = map[string]any{"operationId": "mixed-load-send", "segments": []any{map[string]any{"parts": []any{map[string]any{"type": "text", "text": "Synthetic mixed-load reply."}}}}}
		if targetText != "" {
			var plan struct{ TargetMessageIDs []string }
			if json.Unmarshal(data["plan"], &plan) != nil || len(plan.TargetMessageIDs) != 1 {
				return fmt.Errorf("missing expected delivery target")
			}
			args.(map[string]any)["segments"].([]any)[0].(map[string]any)["replyToId"] = plan.TargetMessageIDs[0]
		}
	default:
		return fmt.Errorf("unexpected fixture model stage")
	}
	message := map[string]any{"role": "assistant", "content": ""}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	message["tool_calls"] = []any{map[string]any{"id": "mixed-" + name, "type": "function", "function": map[string]string{"name": name, "arguments": string(raw)}}}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}})
}
