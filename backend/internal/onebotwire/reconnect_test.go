package onebotwire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestReconnectWhilePreviousConnectFinishes(t *testing.T) {
	t.Run("reporting_ready_with_unacknowledged_send", func(t *testing.T) { testReconnectWhileOpening(t, false) })
	t.Run("qualifying_login_of_dead_socket", func(t *testing.T) { testReconnectWhileOpening(t, true) })
}

func testReconnectWhileOpening(t *testing.T, holdLogin bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstBlocked, release, disconnected, secondReady, drop := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var connections, sends, logins atomic.Int32
	sent := make(chan struct{}, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		attempt := connections.Add(1)
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var request struct{ Action, Echo string }
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			data := map[string]int{"user_id": 42}
			if request.Action != "get_login_info" {
				sends.Add(1)
				if attempt == 1 {
					sent <- struct{}{}
					select {
					case <-drop:
					case <-ctx.Done():
					}
					return
				}
				data = map[string]int{"message_id": 73}
			}
			response, _ := json.Marshal(map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo, "data": data})
			if err := socket.Write(ctx, websocket.MessageText, response); err != nil {
				return
			}
			if holdLogin && attempt == 1 {
				select {
				case <-drop:
				case <-ctx.Done():
				}
				return
			}
		}
	}))
	defer peer.Close()
	const retry = 100 * time.Millisecond
	var readyCount, closedCount atomic.Int32
	ws := NewWS(WSConfig{URL: "ws" + strings.TrimPrefix(peer.URL, "http"), Timeout: 2 * time.Second, Reconnect: retry},
		WSCallbacks{
			Login: func(context.Context, any) error {
				if holdLogin && logins.Add(1) == 1 {
					close(firstBlocked)
					<-release
				}
				return nil
			},
			Reply: func(_ context.Context, event *jsjson.Object, _, _ string) (any, error) {
				data, _ := event.Get("data")
				return data, nil
			},
			Status: func(status string) {
				switch status {
				case "connected":
					if readyCount.Add(1) == 1 && !holdLogin {
						close(firstBlocked)
						<-release
					} else if connections.Load() == 2 {
						close(secondReady)
					}
				case "disconnected":
					if closedCount.Add(1) == 1 {
						close(disconnected)
					}
				}
			},
		})

	defer func() {
		unblock()
		if err := ws.CloseAndWait(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	wait := func(event <-chan struct{}, phase string) {
		t.Helper()
		select {
		case <-event:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", phase)
		}
	}
	connected := make(chan error, 1)
	go func() { connected <- ws.Connect(ctx) }()
	wait(firstBlocked, "first connection barrier")
	wantSends := int32(1)
	if holdLogin {
		close(drop)
		wait(disconnected, "physical disconnect during login validation")
	} else {
		wantSends++
		old := make(chan error, 1)
		go func() {
			_, err := ws.Action(ctx, "send_group_msg", new(jsjson.Object))
			old <- err
		}()
		wait(sent, "old outbound request reaching peer")
		close(drop)
		wait(disconnected, "physical disconnect with pending send")
		select {
		case err := <-old:
			got, ok := err.(*Error)
			if !ok || got.Code != "connection_closed" || !got.Transmitted {
				t.Fatalf("sent without ACK must remain uncertain: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("old outbound request did not settle")
		}
	}
	// Keep the first attempt owned across the retry deadline. The timer must
	// wait for ownership to clear rather than just joining the old result.
	time.Sleep(3 * retry)
	unblock()
	select {
	case err := <-connected:
		if holdLogin {
			got, ok := err.(*Error)
			if !ok || got.Code != "connection_closed" || got.Transmitted {
				t.Fatalf("closed login socket was published as ready: %v", err)
			}
		} else if err != nil {
			t.Fatalf("first connect result: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("first connection did not settle")
	}
	wait(secondReady, "a fresh login on the replacement socket")
	value, err := ws.Action(ctx, "send_group_msg", new(jsjson.Object))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := value.(*jsjson.Object).Get("message_id")
	if id != float64(73) || connections.Load() != 2 || sends.Load() != wantSends {
		t.Fatalf("expected one new ACK and no replay, got receipt=%v connections=%d sends=%d", id, connections.Load(), sends.Load())
	}
}
