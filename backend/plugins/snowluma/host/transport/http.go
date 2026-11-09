// Package snowlumatransport owns physical network work for the bundled WASM
// SnowLuma guest. It is not a second V1/Node host.
package snowlumatransport

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/outboundmedia"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"golang.org/x/text/encoding/unicode"
)

type Error struct {
	Code        string
	Transmitted bool
	HTTPStatus  int
}

func (e *Error) Error() string { return e.Code }
func failure(code string, transmitted bool) error {
	return &Error{Code: code, Transmitted: transmitted}
}

type HTTPConfig struct {
	URL, AccountID, Token, EventToken, EventHost, EventPath string
	EventPort                                               int
	Timeout                                                 time.Duration
}
type HTTPCallbacks struct {
	// Receive runs after native authentication and the guest's account/payload
	// decision. Its actual completion remains owned even after the peer disconnects.
	Receive func(context.Context, string) error
	Status  func(string)
	Error   func(string, error)
}
type connectAttempt struct {
	done chan struct{}
	err  error
}
type HTTP struct {
	mu              sync.Mutex
	config          HTTPConfig
	callbacks       HTTPCallbacks
	guest           func(context.Context, []byte) ([]byte, error)
	life            context.Context
	cancel          context.CancelFunc
	work            quiescence.Group
	client          *http.Client
	transport       *http.Transport
	server          *http.Server
	listener        net.Listener
	ready, disposed bool
	connecting      *connectAttempt
}

func NewHTTP(config HTTPConfig, host *plugins.WASMHost, guestID string, callbacks HTTPCallbacks) (*HTTP, error) {
	if config.EventHost == "" {
		config.EventHost = "127.0.0.1"
	}
	if config.EventPath == "" {
		config.EventPath = "/onebot/events"
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	guest, err := host.BindCall(guestID)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Node fetch does not opt into ambient proxy variables in this source path.
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &HTTP{config: config, callbacks: callbacks, guest: guest, life: life, cancel: cancel, transport: transport, client: client}, nil
}
func (h *HTTP) status(value string) {
	if h.callbacks.Status != nil {
		h.callbacks.Status(value)
	}
}
func (h *HTTP) decision(operation, raw string) (*jsjson.Object, error) {
	input, _ := jsjson.NewObject(jsjson.Member{Key: "operation", Value: operation}, jsjson.Member{Key: "raw", Value: raw}, jsjson.Member{Key: "account", Value: h.config.AccountID})
	wire, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	output, err := h.guest(h.life, wire)
	if err != nil {
		return nil, err
	}
	parsed, err := jsjson.Parse(string(output))
	if err != nil {
		return nil, err
	}
	object, ok := parsed.(*jsjson.Object)
	if !ok {
		return nil, failure("invalid_guest_response", false)
	}
	if code, _ := object.Get("error"); code != nil {
		text, _ := code.(string)
		transmitted, _ := object.Get("transmitted")
		return nil, failure(text, transmitted == true)
	}
	return object, nil
}
func (h *HTTP) actionURL(name string) (string, error) {
	raw, err := jsjson.UTF8Text(h.config.URL)
	if err != nil {
		return "", failure("invalid_http_url", false)
	}
	address, err := whatwg.Parse(raw)
	if err != nil || (address.Protocol() != "http:" && address.Protocol() != "https:") || address.Username() != "" || address.Password() != "" || address.Search() != "" || address.Hash() != "" {
		return "", failure("invalid_http_url", false)
	}
	address.SetPathname(strings.TrimSuffix(address.Pathname(), "/") + "/" + name)
	return address.Href(false), nil
}
func validEventPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, c := range value {
		if c != '/' && c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func (h *HTTP) Connect(ctx context.Context) error {
	h.mu.Lock()
	if h.disposed {
		h.mu.Unlock()
		return failure("adapter_disposed", false)
	}
	if h.ready {
		h.mu.Unlock()
		return nil
	}
	if attempt := h.connecting; attempt != nil {
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-attempt.done:
			return attempt.err
		}
	}
	done, ok := h.work.Start()
	if !ok {
		h.mu.Unlock()
		return failure("adapter_disposed", false)
	}
	attempt := &connectAttempt{done: make(chan struct{})}
	h.connecting = attempt
	h.mu.Unlock()
	defer done()
	err := h.connect(ctx)
	h.mu.Lock()
	attempt.err = err
	h.connecting = nil
	close(attempt.done)
	h.mu.Unlock()
	return err
}
func (h *HTTP) connect(ctx context.Context) error {
	if _, err := h.actionURL("get_login_info"); err != nil {
		return err
	}
	config := h.config
	if (config.EventHost != "127.0.0.1" && config.EventHost != "::1" && config.EventHost != "0.0.0.0" && config.EventHost != "::") || config.EventPort < 0 || config.EventPort > 65535 || !validEventPath(config.EventPath) {
		return failure("invalid_http_event_configuration", false)
	}
	if config.EventToken == "" {
		return failure("event_credential_not_configured", false)
	}
	h.status("connecting")
	data, err := h.action(ctx, "get_login_info", new(jsjson.Object), true, nil)
	if err != nil {
		h.status("disconnected")
		return err
	}
	raw, err := jsjson.Stringify(data)
	if err != nil {
		return err
	}
	if _, err = h.decision("http_login", raw); err != nil {
		h.status("disconnected")
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(config.EventHost, strconv.Itoa(config.EventPort)))
	if err != nil {
		h.status("disconnected")
		return failure("http_event_listen_failed", false)
	}
	listener = &eventListener{Listener: listener}
	server := &http.Server{Handler: http.HandlerFunc(h.event), MaxHeaderBytes: 16384, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second}
	server.SetKeepAlivesEnabled(false)
	h.mu.Lock()
	if h.disposed || ctx.Err() != nil {
		h.mu.Unlock()
		listener.Close()
		return failure("adapter_disposed", false)
	}
	h.listener = listener
	h.server = server
	h.ready = true
	serveDone, _ := h.work.Child()
	h.mu.Unlock()
	go func() {
		defer serveDone()
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.mu.Lock()
			active := !h.disposed
			h.ready = false
			h.mu.Unlock()
			if active {
				h.status("disconnected")
				if h.callbacks.Error != nil {
					h.callbacks.Error("http_event_server_error", failure("http_event_server_error", false))
				}
			}
		}
	}()
	h.status("connected")
	return nil
}
func (h *HTTP) Address() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listener == nil {
		return ""
	}
	return h.listener.Addr().String()
}
func (h *HTTP) Action(ctx context.Context, name string, params *jsjson.Object) (any, error) {
	return h.action(ctx, name, params, false, nil)
}
func (h *HTTP) ActionWithGuard(ctx context.Context, name string, params *jsjson.Object, before func(context.Context) error) (any, error) {
	return h.action(ctx, name, params, false, before)
}
func (h *HTTP) ActionPrepared(ctx context.Context, name string, params *jsjson.Object, preparation Preparation, before func(context.Context) error) (any, error) {
	return h.actionPrepared(ctx, name, params, false, before, &preparation)
}
func (h *HTTP) action(ctx context.Context, name string, params *jsjson.Object, bootstrap bool, before func(context.Context) error) (any, error) {
	return h.actionPrepared(ctx, name, params, bootstrap, before, nil)
}
func (h *HTTP) actionPrepared(ctx context.Context, name string, params *jsjson.Object, bootstrap bool, before func(context.Context) error, preparation *Preparation) (any, error) {
	if ctx.Err() != nil {
		return nil, failure("request_cancelled", false)
	}
	done, ok := h.work.Start()
	if !ok {
		return nil, failure("adapter_not_connected", false)
	}
	defer done()
	h.mu.Lock()
	active := !h.disposed && (bootstrap || h.ready)
	h.mu.Unlock()
	if !active {
		return nil, failure("adapter_not_connected", false)
	}
	address, err := h.actionURL(name)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, h.config.Timeout)
	defer cancel()
	stop := context.AfterFunc(h.life, cancel)
	defer stop()
	if h.life.Err() != nil {
		cancel()
	}
	transmitted := false
	classify := func(err error) error {
		var known *Error
		if errors.As(err, &known) {
			return known
		}
		code := "http_request_failed"
		if requestCtx.Err() != nil {
			code = "request_cancelled"
			if ctx.Err() == nil && h.life.Err() == nil && errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				code = "request_timeout"
			}
		}
		return failure(code, transmitted)
	}
	var wire []byte
	var lease *outboundmedia.Lease
	if preparation != nil {
		lease, err = preparation.Encoder.Encode(requestCtx, params, preparation.Sources)
		if err != nil {
			return nil, encoderFailure(err, requestCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil && h.life.Err() == nil)
		}
		defer lease.Release()
		wire = lease.Bytes
	} else {
		wire, err = jsjson.Marshal(params)
	}
	if err != nil {
		return nil, classify(err)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, address, bytes.NewReader(wire))
	if err != nil {
		return nil, classify(err)
	}
	request.Header.Set("Content-Type", "application/json")

	if preparation != nil {
		if requestCtx.Err() != nil {
			return nil, classify(requestCtx.Err())
		}
		h.mu.Lock()
		active = !h.disposed && (bootstrap || h.ready)
		h.mu.Unlock()
		if !active {
			return nil, failure("adapter_not_connected", false)
		}
	}
	if before != nil {
		if err = before(requestCtx); err != nil {
			return nil, classify(err)
		}
	}
	h.mu.Lock()
	active = !h.disposed && (bootstrap || h.ready)
	h.mu.Unlock()
	if !active {
		return nil, failure("adapter_not_connected", false)
	}
	if requestCtx.Err() != nil {
		return nil, classify(requestCtx.Err())
	}
	transmitted = true
	if h.config.Token != "" {
		token, err := headerByteString("Bearer " + h.config.Token)
		if err != nil {
			return nil, classify(err)
		}
		request.Header.Set("Authorization", token)
	}
	if lease != nil {
		hold, e := lease.Hold()
		if e != nil {
			return nil, classify(e)
		}
		physicalDone, ok := h.work.Child()
		if !ok {
			_ = hold.Close()
			return nil, failure("adapter_disposed", false)
		}
		body := &heldBody{Reader: bytes.NewReader(wire), hold: hold, done: physicalDone}
		request.Body = body
		request.ContentLength = int64(len(wire))
		// Redirects are disabled; never permit an unowned automatic replay.
		request.GetBody = nil
	}
	response, err := h.client.Do(request)
	if err != nil {
		return nil, classify(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &Error{Code: "http_action_failed", Transmitted: true, HTTPStatus: response.StatusCode}
	}
	// This is the source's existing action-response limit, not a new safety cap.
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return nil, classify(err)
	}
	if len(body) > 8*1024*1024 {
		return nil, failure("http_response_too_large", true)
	}
	if requestCtx.Err() != nil {
		return nil, classify(requestCtx.Err())
	}
	decision, err := h.decision("http_response", bufferText(body))
	if err != nil {
		return nil, err
	}
	value, _ := decision.Get("data")
	return value, nil
}
func (h *HTTP) event(w http.ResponseWriter, r *http.Request) {
	done, ok := h.work.Start()
	if !ok {
		w.WriteHeader(503)
		return
	}
	defer done()
	w.Header().Set("Connection", "close")
	finish := func(status int) { w.WriteHeader(status) }
	h.mu.Lock()
	ready := h.ready && !h.disposed
	h.mu.Unlock()
	if !ready {
		finish(503)
		return
	}
	if r.RequestURI != h.config.EventPath || r.Method != http.MethodPost {
		finish(404)
		return
	}
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		finish(415)
		return
	}
	signature := r.Header.Get("X-Signature")
	if len(r.Header.Values("X-Signature")) != 1 || len(r.Header.Values("X-Self-Id")) != 1 || len(signature) != 45 || !strings.HasPrefix(signature, "sha1=") || headerText(r.Header.Get("X-Self-Id")) != h.config.AccountID {
		finish(401)
		return
	}
	supplied, err := hex.DecodeString(signature[5:])
	if err != nil {
		finish(401)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024+1))
	if err != nil {
		return
	}
	if len(body) > 1024*1024 {
		finish(413)
		return
	}
	h.mu.Lock()
	ready = h.ready && !h.disposed
	h.mu.Unlock()
	if !ready {
		finish(503)
		return
	}
	secret, _ := jsjson.UTF8Text(h.config.EventToken)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(supplied, mac.Sum(nil)) {
		finish(401)
		return
	}
	decision, err := h.decision("http_event", bufferText(body))
	if err != nil {
		finish(400)
		return
	}
	status, _ := decision.Get("httpStatus")
	code, ok := status.(float64)
	if !ok {
		finish(400)
		return
	}
	if code != 204 {
		finish(int(code))
		return
	}
	if h.callbacks.Receive != nil {
		if err = h.callbacks.Receive(h.life, bufferText(body)); err != nil {
			// OneBotAdapter.receive swallows normalization/callback errors, while the
			// authenticated event endpoint still acknowledges without a quick action.
			if h.callbacks.Error != nil {
				h.callbacks.Error("invalid_event_or_callback_failed", err)
			}
		}
	}
	finish(204)
}
func (h *HTTP) CloseAndWait(ctx context.Context) error {
	h.mu.Lock()
	first := !h.disposed
	h.disposed = true
	h.ready = false
	server := h.server
	h.mu.Unlock()
	h.work.Seal()
	h.cancel()
	if server != nil {
		_ = server.Close()
	}
	h.transport.CloseIdleConnections()
	if first {
		h.status("disposed")
	}
	return h.work.Wait(ctx)
}

// Buffer.toString('utf8') replaces malformed bytes but preserves a leading BOM.
func bufferText(raw []byte) string {
	decoded, _ := unicode.UTF8.NewDecoder().Bytes(raw)
	return string(decoded)
}
func headerByteString(value string) (string, error) {
	units, err := jsjson.UTF16(value)
	if err != nil {
		return "", err
	}
	bytes := make([]byte, len(units))
	for i, unit := range units {
		if unit > 255 {
			return "", errors.New("invalid_header_value")
		}
		bytes[i] = byte(unit)
	}
	return string(bytes), nil
}
func headerText(value string) string {
	units := make([]uint16, len(value))
	for i := 0; i < len(value); i++ {
		units[i] = uint16(value[i])
	}
	return jsjson.FromUTF16(units)
}

type eventListener struct {
	net.Listener
	mu     sync.Mutex
	active int
}
type eventConnection struct {
	net.Conn
	owner *eventListener
	once  sync.Once
}

func (l *eventListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.active >= 64 {
			l.mu.Unlock()
			connection.Close()
			continue
		}
		l.active++
		l.mu.Unlock()
		return &eventConnection{Conn: connection, owner: l}, nil
	}
}
func (c *eventConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); c.owner.active--; c.owner.mu.Unlock() })
	return err
}
