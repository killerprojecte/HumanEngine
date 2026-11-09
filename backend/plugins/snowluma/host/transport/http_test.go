package snowlumatransport

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestHTTPRealGuestSourceAndPhysicalUnload(t *testing.T) {
	var oracle struct {
		Actions []struct {
			Name, Raw, Error string
			Transmitted      bool
			Value            any
		}
		EventCases []struct {
			Name, Raw, HeaderAccount string
			BadSignature             bool
			Status                   int
		}
		Events                []string
		RequestAuthentication bool
		Statuses              []string
	}
	raw, err := os.ReadFile("testdata/http-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", artifactPath, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("real SnowLuma guest: %v %s", err, out)
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	responses := map[string]string{}
	for _, row := range oracle.Actions {
		responses["/"+row.Name] = row.Raw
	}
	var authenticated atomic.Bool
	authenticated.Store(true)
	actionEntered, actionDone := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "{}" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-action-token" {
			authenticated.Store(false)
		}
		if r.URL.Path == "/hold" {
			close(actionEntered)
			<-r.Context().Done()
			close(actionDone)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/get_login_info" {
			io.WriteString(w, `{"status":"ok","retcode":0,"data":{"user_id":42}}`)
			return
		}
		io.WriteString(w, responses[r.URL.Path])
	}))
	defer upstream.Close()
	var mu sync.Mutex
	var received, statuses []string
	var hold atomic.Bool
	receivedEntered, releaseReceived, cancelObserved := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReceived) }) }
	defer release()
	transport, err := NewHTTP(HTTPConfig{URL: upstream.URL, AccountID: "42", Token: "synthetic-action-token", EventToken: "synthetic-event-token", EventPort: 0}, host, "snowluma", HTTPCallbacks{
		Status: func(status string) { mu.Lock(); statuses = append(statuses, status); mu.Unlock() },
		Receive: func(ctx context.Context, raw string) error {
			if hold.Load() {
				close(receivedEntered)
				<-ctx.Done()
				close(cancelObserved)
				<-releaseReceived
			}
			mu.Lock()
			received = append(received, raw)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Release a deliberately uncooperative callback before cleanup on assertion failure.
	defer func() { release(); transport.CloseAndWait(context.Background()) }()
	if err = transport.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Actions {
		value, err := transport.Action(context.Background(), row.Name, new(jsjson.Object))
		if row.Error != "" {
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != row.Error || typed.Transmitted != row.Transmitted {
				t.Fatalf("%s: %#v", row.Name, err)
			}
		} else {
			got, e := jsjson.Stringify(value)
			expected, _ := json.Marshal(row.Value)
			if err != nil || e != nil || got != string(expected) {
				t.Fatal(row.Name, got, err, e)
			}
		}
	}
	post := func(body, account string, bad bool) (int, error) {
		mac := hmac.New(sha1.New, []byte("synthetic-event-token"))
		mac.Write([]byte(body))
		signature := hex.EncodeToString(mac.Sum(nil))
		if bad {
			signature = strings.Repeat("0", 40)
		}
		request, err := http.NewRequest("POST", "http://"+transport.Address()+"/onebot/events", strings.NewReader(body))
		if err != nil {
			return 0, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Signature", "sha1="+signature)
		request.Header.Set("X-Self-Id", account)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return response.StatusCode, nil
	}
	for _, row := range oracle.EventCases {
		account := row.HeaderAccount
		if account == "" {
			account = "42"
		}
		code, err := post(row.Raw, account, row.BadSignature)
		if err != nil || code != row.Status {
			t.Fatal(row.Name, code, err)
		}
	}
	mu.Lock()
	gotEvents, _ := json.Marshal(received)
	wantEvents, _ := json.Marshal(oracle.Events)
	gotStatuses, _ := json.Marshal(statuses)
	wantStatuses, _ := json.Marshal(oracle.Statuses)
	mu.Unlock()
	if string(gotEvents) != string(wantEvents) || string(gotStatuses) != string(wantStatuses) || authenticated.Load() != oracle.RequestAuthentication {
		t.Fatal("source differential", string(gotEvents), string(gotStatuses), authenticated.Load())
	}
	actionContext, cancel := context.WithCancel(context.Background())
	actionResult := make(chan error, 1)
	go func() { _, err := transport.Action(actionContext, "hold", new(jsjson.Object)); actionResult <- err }()
	select {
	case <-actionEntered:
	case err := <-actionResult:
		t.Fatal("action returned before entering", err)
	case <-time.After(5 * time.Second):
		t.Fatal("action did not enter")
	}
	cancel()
	select {
	case err = <-actionResult:
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != "request_cancelled" || !typed.Transmitted {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled action did not return")
	}
	select {
	case <-actionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("physical HTTP request survived cancellation")
	}
	hold.Store(true)
	eventResult := make(chan error, 1)
	go func() { _, err := post(`{"self_id":42}`, "42", false); eventResult <- err }()
	select {
	case <-receivedEntered:
	case err := <-eventResult:
		t.Fatal("event returned before callback", err)
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not enter")
	}
	closed := make(chan error, 1)
	go func() { closed <- transport.CloseAndWait(context.Background()) }()
	select {
	case <-cancelObserved:
	case err := <-closed:
		t.Fatal("close returned before cancelling callback", err)
	case <-time.After(5 * time.Second):
		t.Fatal("callback not cancelled")
	}
	select {
	case err := <-closed:
		t.Fatal("close ignored physical callback", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("physical callback did not drain")
	}
	select {
	case <-eventResult:
	case <-time.After(5 * time.Second):
		t.Fatal("event connection did not close")
	}
	if _, err = transport.Action(context.Background(), "ok", new(jsjson.Object)); err == nil {
		t.Fatal("disposed transport accepted action")
	}
	if err = host.Disable(context.Background(), "snowluma"); err != nil || host.Loaded("snowluma") {
		t.Fatal("guest did not unload", err)
	}
}
