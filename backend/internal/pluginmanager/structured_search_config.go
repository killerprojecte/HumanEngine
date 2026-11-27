package pluginmanager

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"golang.org/x/net/http/httpguts"
)

var errStructuredProfile = errors.New("external_v2_structured_configuration_invalid")

type structuredRoute struct {
	endpoint   structuredEndpoint
	provider   string
	credential string
	clear      string
	consent    string
	authHeader string
	queryField string
}

type structuredProfile struct {
	routes          map[string]structuredRoute
	defaultProvider string
	combined        bool
}

func (p structuredProfile) routesFor(config *jsjson.Object) []structuredRoute {
	provider, fallback := text(config, "provider"), text(config, "fallbackProvider")
	if provider == "" {
		provider = p.defaultProvider
	}
	primary, found := p.routes[provider]
	if !found || !p.combined && primary.consent != "" && prop(config, primary.consent) != true {
		return nil
	}
	if fallback == "" || fallback == "none" || fallback == provider {
		return []structuredRoute{primary}
	}
	if second, found := p.routes[fallback]; found {
		return []structuredRoute{primary, second}
	}
	return nil
}

func parseStructuredRoute(properties, route *jsjson.Object) (structuredRoute, error) {
	endpoint, err := parseStructuredEndpoint(text(route, "endpoint"))
	out := structuredRoute{endpoint: endpoint, provider: text(route, "provider"), credential: text(route, "credential"), clear: text(route, "clear")}
	out.consent = text(route, "consent")
	out.authHeader = text(route, "authHeader")
	out.queryField = text(route, "queryField")
	providerField, _ := prop(properties, "provider").(*jsjson.Object)
	providers, _ := prop(providerField, "enum").([]any)
	keyField, _ := prop(properties, out.credential).(*jsjson.Object)
	clearField, _ := prop(properties, out.clear).(*jsjson.Object)
	consentField, _ := prop(properties, out.consent).(*jsjson.Object)
	method := text(route, "method")
	if err != nil || out.provider == "" || !slices.Contains(providers, any(out.provider)) ||
		out.credential == "" || out.clear == "" ||
		text(keyField, "type") != "string" || prop(keyField, "secret") != true ||
		text(clearField, "type") != "boolean" ||
		(method != "" && method != http.MethodGet) ||
		(out.queryField != "" && (method != http.MethodGet || out.queryField != "query")) ||
		(out.authHeader != "" && (method != "" || out.authHeader != "X-API-Key")) ||
		(method == http.MethodGet && text(consentField, "type") != "boolean") ||
		(method == "" && out.consent != "") {
		return structuredRoute{}, errStructuredProfile
	}
	return out, nil
}

func (m *Manager) bindStructuredSearch(id string, schema, service *jsjson.Object) (V2AdapterBinding, error) {
	properties, _ := prop(schema, "properties").(*jsjson.Object)
	if m.credentials == nil || properties == nil || text(schema, "type") != "object" ||
		prop(schema, "additionalProperties") != false || prop(schema, "secret") == true {
		return V2AdapterBinding{}, errStructuredProfile
	}
	values := []any{service}
	profile := structuredProfile{routes: map[string]structuredRoute{}}
	if value := prop(service, "routes"); value != nil {
		routes, ok := value.([]any)
		if !ok || len(routes) < 2 {
			return V2AdapterBinding{}, errStructuredProfile
		}
		values, profile.combined = routes, true
		providerField, _ := prop(properties, "provider").(*jsjson.Object)
		profile.defaultProvider = text(providerField, "default")
	}
	clearFor := map[string]string{}
	keyFor := map[string]string{}
	var names []string
	for _, value := range values {
		node, _ := value.(*jsjson.Object)
		route, err := parseStructuredRoute(properties, node)
		if err != nil {
			return V2AdapterBinding{}, err
		}
		if _, found := profile.routes[route.provider]; found {
			return V2AdapterBinding{}, errStructuredProfile
		}
		if clear, found := clearFor[route.credential]; found {
			if clear != route.clear {
				return V2AdapterBinding{}, errStructuredProfile
			}
		} else {
			if _, found := keyFor[route.clear]; found {
				return V2AdapterBinding{}, errStructuredProfile
			}
			clearFor[route.credential], keyFor[route.clear] = route.clear, route.credential
			names = append(names, route.credential)
		}
		profile.routes[route.provider] = route
	}
	for _, field := range properties.Members() {
		node, _ := field.Value.(*jsjson.Object)
		if _, found := clearFor[field.Key]; !found && schemaDeclaresSecret(node) {
			return V2AdapterBinding{}, errStructuredProfile
		}
	}
	policy := BuiltinPolicy{
		Validate: func(_ context.Context, value *jsjson.Object) error {
			if err := pluginconfig.ValidateBuiltin(value, schema); err != nil {
				return &ConfigurationError{Issues: []Issue{{Field: "configuration", Reason: "invalid_value"}}}
			}
			for _, name := range names {
				if !httpguts.ValidHeaderFieldValue(text(value, name)) {
					return &ConfigurationError{Issues: []Issue{{Field: name, Reason: "invalid_value"}}}
				}
			}
			return nil
		},
		CheckStart: func(config *jsjson.Object) error {
			if len(profile.routesFor(config)) == 0 {
				return errPluginProfileUnsupported
			}
			return nil
		},
		Prepare: func(_ context.Context, value, previous *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			safe, _ := pluginconfig.Redact(value, schema)
			config := safe.(*jsjson.Object)
			var changes []credentials.Change
			for _, name := range names {
				clear := clearFor[name]
				config.Delete(clear)
				switch key := text(value, name); {
				case prop(value, clear) == true:
					changes = append(changes, credentials.Change{Name: name, Mutation: &credentials.Mutation{Action: "clear"}})
				case key != "":
					changes = append(changes, credentials.Change{Name: name, Mutation: &credentials.Mutation{Action: "set", Value: key}})
				}
			}
			// Edits without routing fields preserve the source selection.
			for _, name := range []string{"provider", "fallbackProvider", "allowExperimentalHtml"} {
				if _, found := config.Get(name); !found {
					if saved, found := previous.Get(name); found {
						_ = config.Set(name, saved)
					}
				}
			}
			return config, changes, nil
		},
		Secrets: func(ctx context.Context) (*jsjson.Object, error) {
			members := make([]jsjson.Member, 0, len(names))
			for _, name := range names {
				has, err := m.credentials.Has(ctx, "plugin:"+id, name)
				if err != nil {
					return nil, err
				}
				members = append(members, jsjson.Member{Key: name, Value: has})
			}
			return jsjson.NewObject(members...)
		},
	}
	return V2AdapterBinding{Schema: schema, Policy: policy, Start: func(ctx context.Context, in BuiltinRuntimeInput) (BuiltinLifetime, error) {
		return m.startStructuredSearch(ctx, in, profile)
	}}, nil
}
