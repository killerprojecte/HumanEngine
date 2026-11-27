package externaladapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

const wireProfile = "signed-json-http-1"

const rawSchema = `{
	"type": "object",
	"properties": {
		"listen": {"type":"string", "minLength":1},
		"actionUrl": {"type":"string", "minLength":1},
		"accountId": {"type":"string", "minLength":1},
		"botId": {"type":"string", "minLength":1},
		"key": {"type":"string", "secret":true},
		"clearKey": {"type":"boolean"}
	},
	"required": ["listen","actionUrl","accountId","botId"],
	"additionalProperties": false
}`

type BindingPorts struct {
	Runtime   *botruntime.Runtime
	Ingest    func(context.Context, *botruntime.Adapter, dispatch.SceneRef, *jsjson.Object) error
	Recall    func(context.Context, dispatch.SceneRef, string) error
	Stopped   func(string) error
	HasSecret func(context.Context, string, string) (bool, error)
	Original  func(context.Context, string, string) (memeservice.Original, error)
}

func Resolve(ports BindingPorts) pluginmanager.V2AdapterResolver {
	return func(desc pluginmanager.V2AdapterDescriptor) (pluginmanager.V2AdapterBinding, error) {
		if (desc.Wire == oneBotHTTPWire || desc.Wire == oneBotHTTPMediaWire || desc.Wire == oneBotHTTPOutboundWire || desc.Wire == oneBotHTTPHistoryWire || desc.Wire == oneBotForwardWire) && validWireIdentity(desc.AdapterID) {
			return bindOneBotHTTP(ports, desc)
		}
		if desc.Wire != wireProfile || !validWireIdentity(desc.AdapterID) {
			return pluginmanager.V2AdapterBinding{}, errors.New("external_adapter_wire_unsupported")
		}
		value, _ := jsjson.Parse(rawSchema)
		schema := value.(*jsjson.Object)
		owner := ports.Runtime.NewAdapterOwner()
		return pluginmanager.V2AdapterBinding{
			Schema: schema,
			Start: func(ctx context.Context, in pluginmanager.BuiltinRuntimeInput) (pluginmanager.BuiltinLifetime, error) {
				var config storedConfig
				if err := json.Unmarshal(in.Config, &config); err != nil {
					return nil, err
				}
				raw, err := in.CallGuest(ctx, []byte(`{"operation":"adapterProtocol"}`))
				if err != nil {
					return nil, err
				}
				var protocol struct {
					Wire string `json:"wire"`
				}
				if json.Unmarshal(raw, &protocol) != nil || protocol.Wire != desc.Wire {
					return nil, errors.New("external_adapter_wire_mismatch")
				}
				key, found, err := in.Secret(ctx, "key")
				if err != nil {
					return nil, err
				}
				if !found || key == "" {
					return nil, pluginmanager.NewStartupError("credential_not_configured", nil)
				}
				call, err := in.Host.BindCallWithAdmission(in.ID)
				if err != nil {
					return nil, err
				}
				s, err := openHTTP(ctx, in.Life, owner, httpProfile{
					scope: scope{adapter: desc.AdapterID, account: config.Account}, botID: config.Bot,
					listen: config.Listen, action: config.Action, key: []byte(key),
				}, call, sourcePorts{ingest: ports.Ingest, recall: ports.Recall, stopped: ports.Stopped})
				if err != nil {
					return nil, err
				}
				return s, nil
			},
			Policy: pluginmanager.BuiltinPolicy{
				Validate: func(_ context.Context, input *jsjson.Object) error {
					if err := pluginconfig.ValidateBuiltin(input, schema); err != nil {
						return invalidConfig()
					}
					if _, _, err := net.SplitHostPort(text(input, "listen")); err != nil ||
						!validActionURL(text(input, "actionUrl")) || !validWireIdentity(text(input, "accountId")) || !validWireIdentity(text(input, "botId")) {
						return invalidConfig()
					}
					key, _ := input.Get("key")
					clear, _ := input.Get("clearKey")
					if clear == true && key != nil && key != "" {
						return invalidConfig()
					}
					return nil
				},
				Prepare: func(_ context.Context, input, _ *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
					key, _ := input.Get("key")
					clear, _ := input.Get("clearKey")
					input.Delete("key")
					input.Delete("clearKey")
					var mutation *credentials.Mutation
					switch {
					case clear == true:
						mutation = &credentials.Mutation{Action: "clear"}
					case key != nil && key != "":
						mutation = &credentials.Mutation{Action: "set", Value: key}
					}
					if mutation == nil {
						return input, nil, nil
					}
					return input, []credentials.Change{{Name: "key", Mutation: mutation}}, nil
				},
				Secrets: func(ctx context.Context) (*jsjson.Object, error) {
					found, err := ports.HasSecret(ctx, "plugin:"+desc.PluginID, "key")
					if err != nil {
						return nil, err
					}
					return jsjson.NewObject(jsjson.Member{Key: "key", Value: found})
				},
			},
		}, nil
	}
}

type storedConfig struct {
	Listen  string `json:"listen"`
	Action  string `json:"actionUrl"`
	Account string `json:"accountId"`
	Bot     string `json:"botId"`
}

func validActionURL(value string) bool {
	action, err := url.Parse(value)
	return err == nil && (action.Scheme == "http" || action.Scheme == "https") &&
		action.Hostname() != "" && action.User == nil && action.Fragment == ""
}

func invalidConfig() error { return &pluginmanager.ConfigurationError{Issues: []pluginmanager.Issue{}} }
