package externaladapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

// The OneBot HTTP profiles provide the HTTP action and event envelopes and identity ABI.
// The installed guest supplies login, event and action response interpretation.
const (
	oneBotHTTPWire            = "onebot11-http-1"
	oneBotHTTPMediaWire       = "onebot11-http-2"
	oneBotHTTPOutboundWire    = "onebot11-http-3"
	oneBotHTTPHistoryWire     = "onebot11-http-4"
	oneBotForwardWire         = "onebot11-forward-1"
	oneBotActionResponseLimit = 8 * 1024 * 1024
)

const oneBotHTTPSchema = `{
	"type":"object","additionalProperties":false,
	"properties":{
		"transport":{"type":"string","enum":["http"],"default":"http"},
		"url":{"type":"string","minLength":1},
		"accountId":{"type":"string","minLength":1},
		"eventHost":{"type":"string","enum":["127.0.0.1","::1","0.0.0.0","::"],"default":"127.0.0.1"},
		"eventPort":{"type":"integer","minimum":1,"maximum":65535},
		"eventPath":{"type":"string","pattern":"^\\/[a-zA-Z0-9/_-]+$","default":"/onebot/events"},
		"token":{"type":"string","secret":true},
		"eventToken":{"type":"string","secret":true},
		"clearToken":{"type":"boolean"},
		"clearEventToken":{"type":"boolean"}
	},
	"required":["transport","url","accountId","eventPort"]
}`

type oneBotHTTP struct {
	eventPath string
	websocket bool
	media     bool
	outbound  bool
	history   bool
	token     string
	eventKey  []byte
}

type oneBotIngress struct {
	Adapter string          `json:"adapter,omitempty"`
	Account string          `json:"account"`
	Event   json.RawMessage `json:"event"`
}

func bindOneBotHTTP(ports BindingPorts, desc pluginmanager.V2AdapterDescriptor) (pluginmanager.V2AdapterBinding, error) {
	value, _ := jsjson.Parse(oneBotHTTPSchema)
	schema := value.(*jsjson.Object)
	forward := desc.Wire == oneBotForwardWire
	if forward {
		properties, _ := schema.Get("properties")
		transport, _ := properties.(*jsjson.Object).Get("transport")
		field := transport.(*jsjson.Object)
		_ = field.Set("enum", []any{"http", "websocket"})
		_ = field.Set("default", "websocket")
		_ = schema.Set("required", []any{"url", "accountId"})
	}
	useWebSocket := func(config *jsjson.Object) bool {
		transport, present := config.Get("transport")
		// TypeScript saved legacy forward WebSocket settings without transport.
		return transport == "websocket" || forward && !present
	}
	owner := ports.Runtime.NewAdapterOwner()
	return pluginmanager.V2AdapterBinding{
		Schema: schema,
		Start: func(ctx context.Context, in pluginmanager.BuiltinRuntimeInput) (pluginmanager.BuiltinLifetime, error) {
			cfg, err := guestObject(in.Config)
			if err != nil {
				return nil, err
			}
			raw, err := in.CallGuest(ctx, []byte(`{"operation":"adapterProtocol"}`))
			if err != nil {
				return nil, err
			}
			protocol, err := guestObject(raw)
			if err != nil || text(protocol, "wire") != desc.Wire {
				return nil, errors.New("external_adapter_wire_mismatch")
			}
			token, err := oneBotSecret(ctx, in, "token", "credential_not_configured")
			if err != nil {
				return nil, err
			}
			websocket := useWebSocket(cfg)
			var eventToken string
			if !websocket {
				eventToken, err = oneBotSecret(ctx, in, "eventToken", "event_credential_not_configured")
				if err != nil {
					return nil, err
				}
			}
			call, err := in.Host.BindCallWithAdmission(in.ID)
			if err != nil {
				return nil, err
			}
			account, host, route := text(cfg, "accountId"), text(cfg, "eventHost"), text(cfg, "eventPath")
			if host == "" {
				host = "127.0.0.1"
			}
			if route == "" {
				route = "/onebot/events"
			}
			var listen string
			if !websocket {
				port, _ := cfg.Get("eventPort")
				listen = net.JoinHostPort(host, strconv.Itoa(int(port.(float64))))
			}
			history := forward || desc.Wire == oneBotHTTPHistoryWire
			p := httpProfile{scope: scope{adapter: desc.AdapterID, account: account, onebot: true},
				botID: account, listen: listen, action: text(cfg, "url"),
				onebotHTTP: &oneBotHTTP{eventPath: route, websocket: websocket, token: token, eventKey: []byte(eventToken),
					media: desc.Wire != oneBotHTTPWire, outbound: history || desc.Wire == oneBotHTTPOutboundWire, history: history}}

			return openHTTP(ctx, in.Life, owner, p, call, sourcePorts{ingest: ports.Ingest, recall: ports.Recall, stopped: ports.Stopped, original: ports.Original})
		},
		Policy: pluginmanager.BuiltinPolicy{
			Validate: func(_ context.Context, in *jsjson.Object) error {
				if err := pluginconfig.ValidateBuiltin(in, schema); err != nil {
					return invalidConfig()
				}
				address := text(in, "url")
				u, err := url.Parse(address)
				websocket := useWebSocket(in)
				if websocket && (u == nil || u.Scheme != "ws" && u.Scheme != "wss") {
					return &pluginmanager.ConfigurationError{Issues: []pluginmanager.Issue{{Field: "url", Reason: "websocket_scheme_required"}}}
				}
				if err != nil || u.Hostname() == "" || u.User != nil || (!websocket && !validActionURL(address)) || u.RawQuery != "" || u.ForceQuery || strings.Contains(address, "#") || !validWireIdentity(text(in, "accountId")) || strings.Contains(text(in, "eventPath"), "//") {
					return invalidConfig()
				}
				if !websocket {
					if _, exists := in.Get("eventPort"); !exists {
						return &pluginmanager.ConfigurationError{Issues: []pluginmanager.Issue{{Field: "eventPort", Reason: "required"}}}
					}
				}

				for _, names := range oneBotCredentials {
					clear, _ := in.Get(names[1])
					if clear == true && text(in, names[0]) != "" {
						return invalidConfig()
					}
				}
				return nil
			},
			Prepare: func(_ context.Context, input, previous *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
				if useWebSocket(input) {
					_ = input.Set("transport", "websocket")
					for _, key := range [...]string{"eventHost", "eventPort", "eventPath"} {
						input.Delete(key)
						if previous != nil {
							if value, ok := previous.Get(key); ok {
								_ = input.Set(key, value)
							}
						}
					}
					input.Delete("eventToken")
					input.Delete("clearEventToken")
				}
				var changes []credentials.Change
				for _, names := range oneBotCredentials {
					clear, _ := input.Get(names[1])
					if clear == true {
						changes = append(changes, credentials.Change{Name: names[0], Mutation: &credentials.Mutation{Action: "clear"}})
					} else if value := text(input, names[0]); value != "" {
						changes = append(changes, credentials.Change{Name: names[0], Mutation: &credentials.Mutation{Action: "set", Value: value}})
					}
					input.Delete(names[0])
					input.Delete(names[1])
				}
				return input, changes, nil
			},
			Secrets: func(ctx context.Context) (*jsjson.Object, error) {
				o := new(jsjson.Object)
				for _, names := range oneBotCredentials {
					found, err := ports.HasSecret(ctx, "plugin:"+desc.PluginID, names[0])
					if err != nil {
						return nil, err
					}
					_ = o.Set(names[0], found)
				}
				return o, nil
			},
		},
	}, nil
}

var oneBotCredentials = [][2]string{{"token", "clearToken"}, {"eventToken", "clearEventToken"}}

func oneBotSecret(ctx context.Context, in pluginmanager.BuiltinRuntimeInput, name, missing string) (string, error) {
	value, found, err := in.Secret(ctx, name)
	if err != nil {
		return "", err
	}
	if !found || value == "" {
		return "", pluginmanager.NewStartupError(missing, nil)
	}
	return value, nil
}

func (p *oneBotHTTP) eventSignature(r *http.Request, account string) (string, int, bool) {
	if len(r.Header.Values("X-Signature")) != 1 || len(r.Header.Values("X-Self-ID")) != 1 || r.Header.Get("X-Self-ID") != account {
		return "", 0, false
	}
	sig, ok := strings.CutPrefix(r.Header.Get("X-Signature"), "sha1=")
	return sig, sha1.Size, ok
}

func (p *oneBotHTTP) signEvent(raw []byte) []byte {
	mac := hmac.New(sha1.New, p.eventKey)
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}

type actionOriginal struct {
	Part  int
	Index int
}

type oneBotAction struct {
	Action    string
	Params    json.RawMessage
	Originals []actionOriginal
}

func oneBotActionEnvelope(wire []byte) (oneBotAction, error) {
	var envelope oneBotAction
	if err := json.Unmarshal(wire, &envelope); err != nil || envelope.Action == "" || len(envelope.Params) == 0 || strings.IndexFunc(envelope.Action, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) >= 0 {
		return oneBotAction{}, errors.New("external_adapter_action_invalid")
	}
	return envelope, nil
}

func (s *httpSession) oneBotLogin(ctx context.Context, p *oneBotHTTP) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.profile.action, "/")+"/get_login_info", bytes.NewBufferString("{}"))
	if err != nil {
		return pluginmanager.NewStartupError("invalid_http_url", nil)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return pluginmanager.NewStartupError("http_request_failed", nil)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return pluginmanager.NewStartupError("http_action_failed", map[string]any{"httpStatus": res.StatusCode})
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, oneBotActionResponseLimit+1))
	if err != nil || len(raw) > oneBotActionResponseLimit {
		return pluginmanager.NewStartupError("invalid_http_response", nil)
	}
	return s.validateOneBotLogin(ctx, raw)
}

func (s *httpSession) validateOneBotLogin(ctx context.Context, raw []byte) error {
	out, err := s.invoke(ctx, "adapterLogin", struct {
		Account  string          `json:"account"`
		Response json.RawMessage `json:"response"`
	}{s.profile.account, raw})
	if err != nil {
		return pluginmanager.NewStartupError("invalid_http_response", nil)
	}
	result, err := guestObject(out)
	if err != nil {
		return pluginmanager.NewStartupError("invalid_http_response", nil)
	}
	if text(result, "account") != s.profile.account {
		return pluginmanager.NewStartupError("account_mismatch", nil)
	}
	return nil
}
