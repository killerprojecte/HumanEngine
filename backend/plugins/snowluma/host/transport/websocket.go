package snowlumatransport

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/outboundmedia"
	"io"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

type WSConfig struct {
	URL, AccountID, Token string
	Timeout, Reconnect    time.Duration
}
type wsResult struct {
	value any
	err   error
}
type wsTimer struct {
	timer *time.Timer
	done  func()
}

type wsPending struct {
	socket      *websocket.Conn
	result      chan wsResult
	transmitted bool
}

// WS owns physical IO and correlation only. Response and event business decisions
// execute in the exact bound guest instance, not a movable plugin ID.
type WS struct {
	mu              sync.Mutex
	config          WSConfig
	callbacks       HTTPCallbacks
	guest           func(context.Context, []byte) ([]byte, error)
	life            context.Context
	cancel          context.CancelFunc
	work            quiescence.Group
	socket          *websocket.Conn
	connecting      *connectAttempt
	reconnect       *wsTimer
	pending         map[string]*wsPending
	ready, disposed bool
}

func NewWS(config WSConfig, host *plugins.WASMHost, id string, callbacks HTTPCallbacks) (*WS, error) {
	guest, err := host.BindCall(id)
	if err != nil {
		return nil, err
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	if config.Reconnect == 0 {
		config.Reconnect = 3 * time.Second
	}
	if config.Reconnect < 100*time.Millisecond {
		config.Reconnect = 100 * time.Millisecond
	}
	life, cancel := context.WithCancel(context.Background())
	return &WS{config: config, callbacks: callbacks, guest: guest, life: life, cancel: cancel, pending: map[string]*wsPending{}}, nil
}
func (w *WS) status(status string) {
	if w.callbacks.Status != nil {
		w.callbacks.Status(status)
	}
}
func (w *WS) report(code string, err error) {
	w.mu.Lock()
	disposed := w.disposed
	w.mu.Unlock()
	if !disposed && w.callbacks.Error != nil {
		w.callbacks.Error(code, err)
	}
}
func (w *WS) decide(input *jsjson.Object) (*jsjson.Object, error) {
	wire, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := w.guest(w.life, wire)
	if err != nil {
		return nil, err
	}
	value, err := jsjson.ParseBytes(out)
	if err != nil {
		return nil, err
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return nil, failure("invalid_guest_response", false)
	}
	return o, nil
}
func wsObject(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
func (w *WS) Connect(ctx context.Context) error {
	w.mu.Lock()
	if w.disposed {
		w.mu.Unlock()
		return failure("adapter_disposed", false)
	}
	if w.ready {
		w.mu.Unlock()
		return nil
	}
	if attempt := w.connecting; attempt != nil {
		w.mu.Unlock()
		select {
		case <-attempt.done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done, ok := w.work.Start()
	if !ok {
		w.mu.Unlock()
		return failure("adapter_disposed", false)
	}
	attempt := &connectAttempt{done: make(chan struct{})}
	w.connecting = attempt
	w.stopReconnectLocked()
	w.mu.Unlock()
	defer done()
	err := w.open(ctx)
	w.mu.Lock()
	attempt.err = err
	w.connecting = nil
	close(attempt.done)
	w.mu.Unlock()
	return err
}
func (w *WS) open(ctx context.Context) error {
	raw, err := jsjson.UTF8Text(w.config.URL)
	if err != nil {
		return failure("invalid_websocket_url", false)
	}
	u, err := whatwg.Parse(raw)
	if err != nil || (u.Protocol() != "ws:" && u.Protocol() != "wss:") || u.Username() != "" || u.Password() != "" || u.Search() != "" || u.Hash() != "" {
		return failure("invalid_websocket_url", false)
	}
	w.status("connecting")
	call, cancel := context.WithTimeout(ctx, w.config.Timeout)
	stop := context.AfterFunc(w.life, cancel)
	defer stop()
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	headers := http.Header{}
	if w.config.Token != "" {
		token, err := headerByteString("Bearer " + w.config.Token)
		if err != nil {
			return err
		}
		headers.Set("Authorization", token)
	}
	socket, _, err := websocket.Dial(call, u.Href(false), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, HTTPHeader: headers, CompressionMode: websocket.CompressionContextTakeover, CompressionThreshold: 1})
	if err != nil {
		w.scheduleReconnect(nil)
		return failure("connection_failed", false)
	}
	socket.SetReadLimit(8 * 1024 * 1024)
	w.mu.Lock()
	if w.disposed {
		w.mu.Unlock()
		socket.CloseNow()
		return failure("adapter_disposed", false)
	}
	w.socket = socket
	readDone, ok := w.work.Child()
	w.mu.Unlock()
	if !ok {
		socket.CloseNow()
		return failure("adapter_disposed", false)
	}
	go func() { defer readDone(); w.readLoop(socket) }()
	value, err := w.action(ctx, "get_login_info", new(jsjson.Object), true, nil)
	if err == nil {
		encoded, encodeErr := jsjson.Stringify(value)
		if encodeErr != nil {
			err = encodeErr
		} else {
			result, decisionErr := w.decide(wsObject("operation", "http_login", "raw", encoded, "account", w.config.AccountID))
			err = decisionErr
			if err == nil {
				if code, ok := result.Get("error"); ok {
					err = failure(code.(string), false)
				}
			}
		}
	}
	if err != nil {
		socket.CloseNow()
		return err
	}
	w.mu.Lock()
	if w.disposed || w.socket != socket {
		w.mu.Unlock()
		socket.CloseNow()
		return failure("connection_closed", false)
	}
	w.ready = true
	w.mu.Unlock()
	w.status("connected")
	return nil
}
func (w *WS) stopReconnectLocked() {
	if owner := w.reconnect; owner != nil {
		w.reconnect = nil
		if owner.timer.Stop() {
			owner.done()
		}
	}
}
func (w *WS) scheduleReconnect(socket *websocket.Conn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disposed || (socket != nil && w.socket != socket) || w.reconnect != nil {
		return
	}
	done, ok := w.work.Start()
	if !ok {
		return
	}
	owner := &wsTimer{done: done}
	w.reconnect = owner
	owner.timer = time.AfterFunc(w.config.Reconnect, func() {
		defer owner.done()
		w.mu.Lock()
		if w.reconnect != owner || w.disposed {
			w.mu.Unlock()
			return
		}
		w.reconnect = nil
		w.mu.Unlock()
		if err := w.Connect(w.life); err != nil {
			w.report("reconnect_failed", err)
		}
	})
}
func (w *WS) readLoop(socket *websocket.Conn) {
	defer func() {
		socket.CloseNow()
		w.mu.Lock()
		if w.socket != socket {
			w.mu.Unlock()
			return
		}
		w.ready = false
		for echo, p := range w.pending {
			if p.socket == socket {
				delete(w.pending, echo)
				p.result <- wsResult{err: failure("connection_closed", p.transmitted)}
			}
		}
		disposed := w.disposed
		w.mu.Unlock()
		if !disposed {
			w.status("disconnected")
			w.scheduleReconnect(socket)
		}
	}()
	for {
		kind, raw, err := socket.Read(w.life)
		if err != nil {
			var closeError websocket.CloseError
			invalidCloseText := errors.As(err, &closeError) && !utf8.ValidString(closeError.Reason)
			if websocket.CloseStatus(err) == -1 || invalidCloseText || errors.Is(err, websocket.ErrMessageTooBig) {
				w.report("websocket_error", err)
			}
			socket.FinishProtocolClose()
			return
		}
		// Node ws validates text messages; binary payloads are subsequently decoded
		// using Buffer.toString replacement semantics by the original receive path.
		if kind == websocket.MessageText && !utf8.Valid(raw) {
			w.report("websocket_error", failure("invalid_utf8", false))
			_ = socket.Close(websocket.StatusInvalidFramePayloadData, "")
			return
		}
		text := bufferText(raw)
		value, err := jsjson.Parse(text)
		if err != nil {
			w.report("invalid_event_or_callback_failed", err)
			continue
		}
		event, ok := value.(*jsjson.Object)
		if !ok {
			w.report("invalid_event_or_callback_failed", failure("invalid_payload", false))
			continue
		}
		echo, _ := event.Get("echo")
		echoText, _ := echo.(string)
		w.mu.Lock()
		if w.disposed || w.socket != socket {
			w.mu.Unlock()
			return
		}
		pending := w.pending[echoText]
		if pending != nil && pending.socket == socket {
			delete(w.pending, echoText)
		} else {
			pending = nil
		}
		ready := w.ready
		w.mu.Unlock()
		if pending != nil {
			decision, err := w.decide(wsObject("operation", "receive", "event", event, "knownEcho", true))
			if err != nil {
				pending.result <- wsResult{err: err}
				continue
			}
			code, hasError := decision.Get("error")
			if hasError {
				pending.result <- wsResult{err: failure(code.(string), false)}
			} else {
				data, _ := decision.Get("data")
				pending.result <- wsResult{value: data}
			}
			continue
		}
		if ready && w.callbacks.Receive != nil {
			if err := w.callbacks.Receive(w.life, text); err != nil {
				w.report("invalid_event_or_callback_failed", err)
			}
		}
	}
}
func (w *WS) Action(ctx context.Context, name string, params *jsjson.Object) (any, error) {
	return w.action(ctx, name, params, false, nil)
}
func (w *WS) ActionWithGuard(ctx context.Context, name string, params *jsjson.Object, before func() error) (any, error) {
	return w.action(ctx, name, params, false, before)
}
func (w *WS) ActionPrepared(ctx context.Context, name string, params *jsjson.Object, preparation Preparation, before func() error) (any, error) {
	return w.actionPrepared(ctx, name, params, false, before, &preparation)
}
func (w *WS) action(ctx context.Context, name string, params *jsjson.Object, bootstrap bool, before func() error) (any, error) {
	return w.actionPrepared(ctx, name, params, bootstrap, before, nil)
}
func (w *WS) actionPrepared(ctx context.Context, name string, params *jsjson.Object, bootstrap bool, before func() error, preparation *Preparation) (any, error) {
	if ctx.Err() != nil {
		return nil, failure("request_cancelled", false)
	}
	done, ok := w.work.Start()
	if !ok {
		return nil, failure("adapter_not_connected", false)
	}
	defer done()
	w.mu.Lock()
	socket := w.socket
	if w.disposed || socket == nil || (!bootstrap && !w.ready) {
		w.mu.Unlock()
		return nil, failure("adapter_not_connected", false)
	}
	w.mu.Unlock()
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return nil, err
	}
	random[6] = (random[6] & 15) | 64
	random[8] = (random[8] & 63) | 128
	echo := fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:])
	var wire []byte
	var err error
	var lease *outboundmedia.Lease
	callerCtx := ctx
	if preparation != nil {
		encodingCtx, cancel := context.WithTimeout(ctx, w.config.Timeout)
		stop := context.AfterFunc(w.life, cancel)
		if w.life.Err() != nil {
			cancel()
		}
		lease, err = preparation.Encoder.Encode(encodingCtx, wsObject("action", name, "params", params, "echo", echo), preparation.Sources)
		timedOut := encodingCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil && w.life.Err() == nil
		defer func() { stop(); cancel() }()
		ctx = encodingCtx
		if err != nil {
			return nil, encoderFailure(err, timedOut)
		}
		defer lease.Release()
		wire = lease.Bytes
		if ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded && callerCtx.Err() == nil && w.life.Err() == nil {
				return nil, failure("request_timeout", false)
			}
			return nil, failure("request_cancelled", false)
		}
		w.mu.Lock()
		active := !w.disposed && w.socket == socket && (bootstrap || w.ready)
		w.mu.Unlock()
		if !active {
			return nil, failure("adapter_not_connected", false)
		}
	} else {
		wire, err = jsjson.Marshal(wsObject("action", name, "params", params, "echo", echo))
	}
	if err != nil {
		return nil, err
	}
	if before != nil {
		if err := before(); err != nil {
			return nil, failure("socket_send_failed", false)
		}
	}
	if ctx.Err() != nil {
		return nil, failure("socket_send_failed", false)
	}
	pending := &wsPending{socket: socket, result: make(chan wsResult, 1)}
	w.mu.Lock()
	if w.disposed || w.socket != socket || (!bootstrap && !w.ready) {
		w.mu.Unlock()
		return nil, failure("socket_send_failed", false)
	}
	pending.transmitted = true
	w.pending[echo] = pending
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.pending[echo] == pending {
			delete(w.pending, echo)
		}
		w.mu.Unlock()
	}()
	// Caller cancellation settles the action but must not pretend that a write
	// already accepted by the socket has physically finished. The writer remains
	// separately counted until Write returns or socket termination interrupts it.
	writeDone, ok := w.work.Child()
	if !ok {
		return nil, failure("adapter_disposed", false)
	}
	var hold io.Closer
	if lease != nil {
		hold, err = lease.Hold()
		if err != nil {
			writeDone()
			return nil, failure("socket_send_failed", false)
		}
	}
	go func() {
		defer writeDone()
		if hold != nil {
			defer hold.Close()
		}
		if err := socket.Write(w.life, websocket.MessageText, wire); err != nil {
			w.mu.Lock()
			if w.pending[echo] == pending {
				delete(w.pending, echo)
				pending.result <- wsResult{err: failure("socket_send_failed", true)}
			}
			w.mu.Unlock()
		}
	}()
	timer := time.NewTimer(w.config.Timeout)
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.value, result.err
	case <-ctx.Done():
		if preparation != nil && ctx.Err() == context.DeadlineExceeded && callerCtx.Err() == nil && w.life.Err() == nil {
			return nil, failure("request_timeout", true)
		}
		return nil, failure("request_cancelled", true)
	case <-w.life.Done():
		return nil, failure("request_cancelled", true)
	case <-timer.C:
		return nil, failure("request_timeout", true)
	}
}
func (w *WS) CloseAndWait(ctx context.Context) error {
	w.mu.Lock()
	first := !w.disposed
	w.disposed = true
	w.ready = false
	w.work.Seal()
	w.stopReconnectLocked()
	socket := w.socket
	// Source lifetime.abort synchronously settles accepted requests before
	// socket termination can report a later connection_closed event.
	for echo, pending := range w.pending {
		delete(w.pending, echo)
		pending.result <- wsResult{err: failure("request_cancelled", pending.transmitted)}
	}
	w.cancel()
	w.mu.Unlock()
	if socket != nil {
		socket.CloseNow()
	}
	err := w.work.Wait(ctx)
	if err == nil && first {
		w.status("disposed")
	}
	return err
}
