package externaladapter

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestIndependentLetterboxWASMWireAndPhysicalRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "letterbox.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "./testdata/letterbox")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("wasm: %v %s", err, output)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	wasm, err := plugins.NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer wasm.Close(context.Background())
	if err = wasm.Enable(ctx, "fixture-letterbox", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := wasm.BindCallWithAdmission("fixture-letterbox")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("synthetic-external-transport-key")
	var requests, calls atomic.Int32
	var wrongAck atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/deliver" ||
			r.Header.Get(signatureHeader) != hex.EncodeToString(sign(key, "action", raw)) {
			t.Errorf("invalid real wire: %s, %v", raw, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var translated struct {
			Box       string
			InReplyTo string
			Contents  []map[string]string
		}
		if json.Unmarshal(raw, &translated) != nil || translated.Box != "mailbox" || translated.InReplyTo != "letter-1" ||
			len(translated.Contents) != 1 || translated.Contents[0]["text"] != "reply" {
			t.Errorf("wrong guest request: %s", raw)
		}
		ack := []byte(`{"ticket":"ack-1"}`)
		purpose := "receipt"
		if wrongAck.Load() {
			purpose = "event"
		}
		w.Header().Set(signatureHeader, hex.EncodeToString(sign(key, purpose, ack)))
		_, _ = w.Write(ack)
	}))
	defer remote.Close()

	engine := new(botruntime.Runtime)
	holder := engine.NewAdapterOwner()
	profile := httpProfile{scope: scope{adapter: "installed-letterbox", account: "actual-account"}, botID: "letterbot",
		listen: "127.0.0.1:0", action: remote.URL + "/deliver", key: key}
	inside, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var ingested, stopped atomic.Int32
	source := sourcePorts{
		ingest: func(_ context.Context, id dispatch.SceneRef, m *jsjson.Object) error {
			origin, _ := m.Get("origin")
			message, _ := m.Get("id")
			if id.AccountID != "actual-account" || id.AdapterID != "installed-letterbox" || id.PlatformSceneID != "mailbox" || origin != "live" || message != messageID(id, "letter-1") {
				t.Errorf("source scope changed: %v %v %v", id, origin, message)
			}
			if ingested.Add(1) == 2 {
				close(inside)
				<-release
			}
			return nil
		},
		recall:  func(context.Context, dispatch.SceneRef, string) error { return errors.New("unexpected_recall") },
		stopped: func(string) error { stopped.Add(1); return nil },
	}
	s, err := openHTTP(ctx, ctx, holder, profile, func(a, b context.Context, v []byte) ([]byte, error) {
		calls.Add(1)
		return call(a, b, v)
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	competing := engine.NewAdapterOwner()
	if _, err = competing.Claim(profile.adapter); !errors.Is(err, botruntime.ErrAdapterIDInUse) {
		t.Fatal("collision", err)
	}
	post := func(purpose string) (int, error) {
		raw := []byte(`{"letter":"letter-1","box":"mailbox","writer":"outside","payload":"first","stamp":"2026-11-25T06:00:00Z"}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+s.listener.Addr().String()+"/event", bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		req.Header.Set(signatureHeader, hex.EncodeToString(sign(key, purpose, raw)))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		return res.StatusCode, nil
	}
	if status, err := post("action"); status != 401 || err != nil || calls.Load() != 0 {
		t.Fatalf("replayed direction ran the guest: %d %v", status, err)
	}
	if status, err := post("event"); status != 204 || err != nil || ingested.Load() != 1 {
		t.Fatalf("valid wire failed: %d %v", status, err)
	}

	scene, err := profile.scene(peer{"group", "mailbox"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := messageID(scene, "letter-1")
	segment := dispatch.Segment{ReplyToID: &sourceID, Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"reply"}`)}}
	services := dispatch.Services{
		Writer:     dispatch.FlowWriterFunc(func(dispatch.FlowEvent) {}),
		Eligible:   func(*dispatch.TurnContext) bool { return true },
		IsRecalled: func(string, string) bool { return false },
		Adapter:    func(string) *dispatch.Adapter { return s.adapter.Transport },
	}
	guard := dispatch.Guard{CheckGrounding: func([]dispatch.Segment) bool { return false },
		Validate: func(dispatch.ValidationRequest, *dispatch.Signal) *dispatch.Future[bool] {
			return dispatch.Resolved(true)
		}}
	send := func() (dispatch.Receipt, *dispatch.Failure) {
		return dispatch.DispatchSegment(services, &dispatch.TurnContext{Scene: scene, Mode: "real", TurnID: "synthetic", Signal: dispatch.NewSignal()}, segment, "wire", guard, nil, 0, 1)
	}
	receipt, failure := send()
	if failure != nil || receipt.Status != dispatch.Sent || receipt.MessageID == nil || *receipt.MessageID != messageID(scene, "ack-1") || receipt.AckOwner() != s.adapter.Transport {
		t.Fatalf("missing real dispatch custody: %+v %v", receipt, failure)
	}
	wrongAck.Store(true)
	receipt, failure = send()
	if failure != nil || receipt.Status != dispatch.Unknown || receipt.MessageID != nil || requests.Load() != 2 {
		t.Fatalf("untrusted ACK or resend: %+v %v (%d)", receipt, failure, requests.Load())
	}

	posted := make(chan struct{})
	go func() { defer close(posted); _, _ = post("event") }()
	select {
	case <-inside:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	defer stop()
	if err = s.CloseAndWait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("claimed early physical drain", err)
	}
	if _, err = competing.Claim(profile.adapter); !errors.Is(err, botruntime.ErrAdapterIDInUse) || stopped.Load() != 0 {
		t.Fatal("released during source callback", err)
	}
	close(release)
	<-posted
	if err = s.CloseAndWait(ctx); err != nil || stopped.Load() != 1 {
		t.Fatal("did not join source and stop callbacks", err)
	}
	next, err := competing.Claim(profile.adapter)
	if err != nil {
		t.Fatal("withheld permanently", err)
	}
	next.Release()
}
