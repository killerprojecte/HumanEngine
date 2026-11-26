package externaladapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/outboundmedia"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
)

const signatureHeader = "X-HumanEngine-Signature"

// An HTTP profile is chosen by the host, not emitted as a URL or credential by
// the WASI guest. No guest method receives a credential, socket or SQL owner.
type httpProfile struct {
	scope
	botID      string
	listen     string
	action     string
	key        []byte
	onebotHTTP *oneBotHTTP
}

type guestCall func(context.Context, context.Context, []byte) ([]byte, error)

type sourcePorts struct {
	ingest   func(context.Context, *botruntime.Adapter, dispatch.SceneRef, *jsjson.Object) error
	recall   func(context.Context, dispatch.SceneRef, string) error
	stopped  func(string) error
	original func(context.Context, string, string) (memeservice.Original, error)
}

type httpSession struct {
	profile  httpProfile
	call     guestCall
	source   sourcePorts
	life     context.Context
	cancel   context.CancelFunc
	lease    *botruntime.AdapterRegistration
	client   *http.Client
	encoder  *outboundmedia.Encoder
	server   http.Server
	listener net.Listener
	adapter  botruntime.Adapter
	work     quiescence.Group
	stop     sync.Once
	served   chan struct{}
	done     chan struct{}
	closeErr error
}

func openHTTP(ctx, life context.Context, owner *botruntime.AdapterOwner, profile httpProfile, call guestCall, source sourcePorts) (*httpSession, error) {
	lease, err := owner.Claim(profile.adapter)
	if err != nil {
		return nil, err
	}
	config := net.ListenConfig{}
	listener, err := config.Listen(ctx, "tcp", profile.listen)
	if err != nil {
		lease.Release()
		return nil, errors.New("external_adapter_listen_failed")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	bound, cancel := context.WithCancel(life)
	s := &httpSession{
		profile: profile, call: call, source: source, life: bound, cancel: cancel,
		lease: lease, listener: listener, served: make(chan struct{}), done: make(chan struct{}),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if p := profile.onebotHTTP; p != nil {
		s.client.Timeout = 20 * time.Second
		s.server.SetKeepAlivesEnabled(false)
		s.server.MaxHeaderBytes = 16384
		s.server.ReadHeaderTimeout = 10 * time.Second
		s.server.ReadTimeout = 15 * time.Second
		if err := s.oneBotLogin(ctx, p); err != nil {
			cancel()
			_ = listener.Close()
			s.client.CloseIdleConnections()
			lease.Release()
			return nil, err
		}
	}
	s.server.BaseContext = func(net.Listener) context.Context { return bound }
	s.server.Handler = http.HandlerFunc(s.receive)
	s.adapter = botruntime.Adapter{
		BotUserID: profile.sender(profile.botID),
		Status: func(context.Context) (string, error) {
			if bound.Err() != nil {
				return "disposed", nil
			}
			return "connected", nil
		},
		Dispose:   cancel,
		Transport: dispatch.AbortContainedAdapter(&dispatch.Adapter{Send: s.send}),
	}
	if p := profile.onebotHTTP; p != nil && p.outbound {
		s.encoder, err = outboundmedia.NewEncoder()
		if err != nil {
			cancel()
			_ = listener.Close()
			s.client.CloseIdleConnections()
			lease.Release()
			return nil, err
		}
	}
	if p := profile.onebotHTTP; p != nil && p.media {
		s.adapter.InlineImage = &botruntime.InlineImageAdapter{
			Read: readOneBotInline, Classify: classifyOneBotImage,
			Current: func() bool { return bound.Err() == nil },
		}
		s.adapter.BrowserImage = s.readOneBotImage
		s.adapter.PrepareVisionImage = s.prepareOneBotImage
	}
	if p := profile.onebotHTTP; p != nil && p.history {
		s.adapter.History = func(ctx context.Context, scene botruntime.Scene, limit int, settle func(*jsjson.Object, error)) {
			s.queryOneBotHistory(ctx, scene.SceneRef, limit, "", nil, settle)
		}
		s.adapter.GetMessage = func(ctx context.Context, scene botruntime.Scene, id string, settle func(*jsjson.Object, error)) {
			s.queryOneBotHistory(ctx, scene.SceneRef, 0, id, nil, settle)
		}
		s.adapter.BrowserMessage = func(ctx context.Context, scene botruntime.Scene, id string, before func(context.Context) error, settle func(*jsjson.Object, error)) {
			s.queryOneBotHistory(ctx, scene.SceneRef, 0, id, before, settle)
		}
	}
	if err := lease.Publish(&s.adapter); err != nil {
		if s.encoder != nil {
			s.encoder.Dispose()
		}
		cancel()
		_ = listener.Close()
		lease.Release()
		return nil, err
	}
	go func() {
		defer close(s.served)
		_ = s.server.Serve(listener)
		s.cancel()
	}()
	context.AfterFunc(bound, s.Dispose)
	return s, nil
}

func (s *httpSession) Dispose() {
	s.stop.Do(func() {
		s.cancel()
		s.work.Seal()
		if s.encoder != nil {
			s.encoder.Dispose()
		}
		s.lease.Detach()
		_ = s.listener.Close()
		_ = s.server.Close()
		s.client.CloseIdleConnections()
		go func() {
			<-s.served
			s.closeErr = errors.Join(s.work.Wait(context.Background()), s.source.stopped(s.profile.adapter))
			if s.encoder != nil {
				s.closeErr = errors.Join(s.closeErr, s.encoder.CloseAndWait(context.Background()))
			}
			if s.closeErr == nil {
				s.lease.Release()
			}
			close(s.done)
		}()
	})
}

func (s *httpSession) CloseAndWait(ctx context.Context) error {
	s.Dispose()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.closeErr
	}
}

func sign(key []byte, purpose string, raw []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, purpose+"\x00")
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}

func (s *httpSession) receive(w http.ResponseWriter, r *http.Request) {
	path := "/event"
	if p := s.profile.onebotHTTP; p != nil {
		path = p.eventPath
	}
	if (s.profile.onebotHTTP != nil && r.RequestURI != path || r.URL.Path != path) || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	done, ok := s.work.Start()
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer done()
	signature := r.Header.Get(signatureHeader)
	width := sha256.Size
	if p := s.profile.onebotHTTP; p != nil {
		var ok bool
		signature, width, ok = p.eventSignature(r, s.profile.account)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	present, err := hex.DecodeString(signature)
	if err != nil || len(present) != width {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var reader io.Reader = r.Body
	if s.profile.onebotHTTP != nil {
		if strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]) != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		reader = io.LimitReader(reader, 1024*1024+1)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.profile.onebotHTTP != nil && len(raw) > 1024*1024 {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	var expected []byte
	if p := s.profile.onebotHTTP; p != nil {
		expected = p.signEvent(raw)
	} else {
		expected = sign(s.profile.key, "event", raw)
	}
	if !hmac.Equal(present, expected) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var input any = json.RawMessage(raw)
	p := s.profile.onebotHTTP
	if p != nil {
		envelope := oneBotIngress{Account: s.profile.account, Event: json.RawMessage(raw)}
		if p.media {
			envelope.Adapter = s.profile.adapter
		}
		input = envelope
	}
	output, err := s.invoke(r.Context(), "adapterIngress", input)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	ev, err := projectEvent(output, p != nil && p.media)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	if ev.Type == "ignore" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	scene, err := s.profile.scene(ev.Peer)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	switch ev.Type {
	case "message":
		message, e := s.profile.message(scene, ev, time.Now())
		if e != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		err = s.source.ingest(r.Context(), &s.adapter, scene, message)
	case "recall":
		err = s.source.recall(r.Context(), scene, messageID(scene, ev.Message))
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *httpSession) invoke(wait context.Context, op string, input any) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Input     any    `json:"input"`
	}{op, input})
	if err != nil {
		return nil, err
	}
	raw, err = s.call(wait, s.life, raw)
	if err != nil {
		return nil, err
	}
	if err = wait.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *httpSession) send(scene dispatch.SceneRef, segment dispatch.Segment, signal *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
	future := dispatch.NewFuture[dispatch.Receipt]()
	done, ok := s.work.Start()
	if !ok {
		future.Reject(errors.New("external_adapter_stopped"))
		return future
	}
	go func() {
		defer done()
		ctx, cancel := context.WithCancel(signal)
		detach := context.AfterFunc(s.life, cancel)
		defer func() { detach(); cancel() }()
		result, err := s.transmit(ctx, scene, segment, before)
		if err != nil {
			future.Reject(err)
		} else {
			future.Resolve(result)
		}
	}()
	return future
}

func (s *httpSession) transmit(ctx context.Context, scene dispatch.SceneRef, segment dispatch.Segment, before func() error) (dispatch.Receipt, error) {
	known, err := s.profile.scene(peer{Kind: scene.Kind, ID: scene.PlatformSceneID})
	if err != nil || scene.ID != known.ID || scene.AdapterID != known.AdapterID || scene.AccountID != known.AccountID {
		return dispatch.Receipt{}, errors.New("external_adapter_send_scope_mismatch")
	}
	var reply string
	if segment.ReplyToID != nil {
		reply, err = platformMessage(scene, *segment.ReplyToID)
		if err != nil {
			return dispatch.Receipt{}, err
		}
	}
	body, err := s.invoke(ctx, "adapterSend", struct {
		Peer    peer              `json:"peer"`
		Parts   []json.RawMessage `json:"parts"`
		ReplyTo string            `json:"replyTo,omitempty"`
	}{peer{Kind: known.Kind, ID: known.PlatformSceneID}, segment.Parts, reply})
	if err != nil {
		return dispatch.Receipt{}, err
	}
	address := s.profile.action
	var encoded *outboundmedia.Lease
	var originals []resolvedOriginal
	if p := s.profile.onebotHTTP; p != nil {
		action, err := oneBotActionEnvelope(body)
		if err != nil {
			return dispatch.Receipt{}, err
		}
		address, body = strings.TrimRight(address, "/")+"/"+action.Action, action.Params
		if len(action.Originals) != 0 {
			if !p.outbound {
				return dispatch.Receipt{}, errors.New("external_adapter_action_invalid")
			}
			encoded, originals, err = s.prepareOriginals(ctx, known, segment, action)
			if err != nil {
				return dispatch.Receipt{}, err
			}
			defer encoded.Release()
			body = encoded.Bytes
		}
	} else if !json.Valid(body) {
		return dispatch.Receipt{}, errors.New("external_adapter_action_invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return dispatch.Receipt{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if p := s.profile.onebotHTTP; p != nil {
		request.Header.Set("Authorization", "Bearer "+p.token)
	} else {
		request.Header.Set(signatureHeader, hex.EncodeToString(sign(s.profile.key, "action", body)))
	}
	if before != nil {
		if err = before(); err != nil {
			return dispatch.Receipt{}, err
		}
	}
	if encoded != nil {
		if err := s.qualifyOriginals(ctx, known.ID, originals); err != nil {
			return dispatch.Receipt{}, err
		}
	}
	if err := s.life.Err(); err != nil {
		return dispatch.Receipt{}, err
	}
	if encoded != nil {
		if err := encoded.BindRequest(request, &s.work); err != nil {
			return dispatch.Receipt{}, err
		}
	}
	witness := dispatch.Receipt{Status: dispatch.Unknown}
	response, err := s.client.Do(request)
	if err != nil {
		return witness, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return witness, nil
	}
	var reader io.Reader = response.Body
	if s.profile.onebotHTTP != nil {
		reader = io.LimitReader(reader, oneBotActionResponseLimit+1)
	}
	raw, err := io.ReadAll(reader)
	if err != nil || s.profile.onebotHTTP != nil && len(raw) > oneBotActionResponseLimit {
		return witness, nil
	}
	if s.profile.onebotHTTP == nil {
		signed, err := hex.DecodeString(response.Header.Get(signatureHeader))
		if err != nil || !hmac.Equal(signed, sign(s.profile.key, "receipt", raw)) {
			return witness, nil
		}
	}
	raw, err = s.invoke(ctx, "adapterReceipt", json.RawMessage(raw))
	if err != nil {
		return witness, nil
	}
	ack, err := guestObject(raw)
	if err != nil {
		return witness, nil
	}
	platform := text(ack, "message")
	if !validWireIdentity(platform) {
		return witness, nil
	}
	id := messageID(known, platform)
	return dispatch.Receipt{Status: dispatch.Sent, MessageID: &id}, nil
}
