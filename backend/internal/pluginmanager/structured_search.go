package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"golang.org/x/net/http/httpguts"
)

type structuredRoute struct {
	endpoint   structuredEndpoint
	provider   string
	credential string
	clear      string
	consent    string
	authHeader string
}

func (m *Manager) bindStructuredSearch(id string, schema, route *jsjson.Object) (V2AdapterBinding, error) {
	endpoint, err := parseStructuredEndpoint(text(route, "endpoint"))
	out := structuredRoute{endpoint: endpoint, provider: text(route, "provider"), credential: text(route, "credential"), clear: text(route, "clear")}
	out.consent = text(route, "consent")
	out.authHeader = text(route, "authHeader")
	properties, _ := prop(schema, "properties").(*jsjson.Object)
	providerField, _ := prop(properties, "provider").(*jsjson.Object)
	providers, _ := prop(providerField, "enum").([]any)
	keyField, _ := prop(properties, out.credential).(*jsjson.Object)
	clearField, _ := prop(properties, out.clear).(*jsjson.Object)
	consentField, _ := prop(properties, out.consent).(*jsjson.Object)
	method := text(route, "method")
	if err != nil || m.credentials == nil || out.provider == "" || !slices.Contains(providers, any(out.provider)) ||
		out.credential == "" || out.clear == "" ||
		text(schema, "type") != "object" || prop(schema, "additionalProperties") != false || prop(schema, "secret") == true ||
		text(keyField, "type") != "string" || prop(keyField, "secret") != true ||
		text(clearField, "type") != "boolean" ||
		(method != "" && method != http.MethodGet) ||
		(out.authHeader != "" && (method != "" || out.authHeader != "X-API-Key")) ||
		(method == http.MethodGet && text(consentField, "type") != "boolean") ||
		(method == "" && out.consent != "") {
		return V2AdapterBinding{}, errors.New("external_v2_structured_configuration_invalid")
	}
	for _, field := range properties.Members() {
		node, _ := field.Value.(*jsjson.Object)
		if field.Key != out.credential && schemaDeclaresSecret(node) {
			return V2AdapterBinding{}, errors.New("external_v2_structured_configuration_invalid")
		}
	}
	policy := BuiltinPolicy{
		Validate: func(_ context.Context, value *jsjson.Object) error {
			if err := pluginconfig.ValidateBuiltin(value, schema); err != nil {
				return &ConfigurationError{Issues: []Issue{{Field: "configuration", Reason: "invalid_value"}}}
			}
			if value := text(value, out.credential); !httpguts.ValidHeaderFieldValue(value) {
				return &ConfigurationError{Issues: []Issue{{Field: out.credential, Reason: "invalid_value"}}}
			}
			return nil
		},
		CheckStart: func(config *jsjson.Object) error {
			provider, fallback := text(config, "provider"), text(config, "fallbackProvider")
			// An implicit TypeScript default is Bing, not this endpoint.
			// Never substitute the preview for an absent or different route.
			if provider != out.provider || fallback != "" && fallback != "none" && fallback != out.provider ||
				out.consent != "" && prop(config, out.consent) != true {
				return errPluginProfileUnsupported
			}
			return nil
		},
		Prepare: func(_ context.Context, value, previous *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			var changes []credentials.Change
			switch {
			case prop(value, out.clear) == true:
				changes = []credentials.Change{{Name: out.credential, Mutation: &credentials.Mutation{Action: "clear"}}}
			case text(value, out.credential) != "":
				changes = []credentials.Change{{Name: out.credential, Mutation: &credentials.Mutation{Action: "set", Value: text(value, out.credential)}}}
			}
			safe, _ := pluginconfig.Redact(value, schema)
			config := safe.(*jsjson.Object)
			config.Delete(out.clear)
			// An edit without routing fields preserves the existing route,
			// including an implicit source default, for rollback.
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
			has, err := m.credentials.Has(ctx, "plugin:"+id, out.credential)
			if err != nil {
				return nil, err
			}
			return jsjson.NewObject(jsjson.Member{Key: out.credential, Value: has})
		},
	}
	return V2AdapterBinding{Schema: schema, Policy: policy, Start: func(ctx context.Context, in BuiltinRuntimeInput) (BuiltinLifetime, error) {
		return m.startStructuredSearch(ctx, in, out)
	}}, nil
}

func (m *Manager) startStructuredSearch(ctx context.Context, in BuiltinRuntimeInput, route structuredRoute) (BuiltinLifetime, error) {
	config := struct {
		MaxResults     int `json:"maxResults"`
		TimeoutMS      int `json:"timeoutMs"`
		ParserBudget   int `json:"maxResponseBytes"`
		MaxConcurrency int `json:"maxConcurrency"`
	}{MaxResults: 5, TimeoutMS: 10000, ParserBudget: 262144, MaxConcurrency: 2}
	if err := json.Unmarshal(in.Config, &config); err != nil || config.MaxResults < 1 || config.MaxResults > 10 ||
		config.TimeoutMS < 500 || config.TimeoutMS > 30000 || config.ParserBudget < 4096 ||
		config.ParserBudget > 16777216 || config.MaxConcurrency < 1 || config.MaxConcurrency > 4 {
		return nil, NewStartupError("invalid_plugin_configuration", nil)
	}
	var key string
	if route.consent == "" {
		var found bool
		var err error
		key, found, err = in.Secret(ctx, route.credential)
		if err != nil {
			return nil, NewStartupError(err.Error(), nil)
		}
		if !found || key == "" || !httpguts.ValidHeaderFieldValue(key) {
			return nil, NewStartupError("credential_not_configured", nil)
		}
	}
	definitions, err := in.CallGuest(ctx, []byte(`{"operation":"search_registrations"}`))
	if err != nil {
		return nil, err
	}
	call, err := m.wasm.BindCallWithAdmission(in.ID)
	if err != nil {
		return nil, err
	}
	owner := newStructuredTransport(in.Life, route.endpoint, route.authHeader, key)
	request, defaultType := owner.post, "application/json"
	if route.consent != "" {
		request, defaultType = owner.get, "text/html"
	}
	var active atomic.Int32
	_, err = m.InstallBuiltinTools(in, definitions, func(ctx context.Context, _ string, raw json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		for {
			n := active.Load()
			if int(n) >= config.MaxConcurrency {
				return []byte(`{"status":"busy","ok":false}`), nil
			}
			if active.CompareAndSwap(n, n+1) {
				break
			}
		}
		defer active.Add(-1)
		timed, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutMS)*time.Millisecond)
		defer cancel()
		planInput, err := json.Marshal(struct {
			Operation  string          `json:"operation"`
			Provider   string          `json:"provider"`
			MaxResults int             `json:"maxResults"`
			Input      json.RawMessage `json:"input"`
		}{"search_plan", route.provider, config.MaxResults, raw})
		if err != nil {
			return nil, err
		}
		reply, err := call(timed, in.Life, planInput)
		if err != nil {
			return nil, err
		}
		var plan struct {
			Limit int             `json:"limit"`
			Body  json.RawMessage `json:"body"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(reply, &plan); err != nil || plan.Error != "" || len(plan.Body) == 0 ||
			plan.Limit < 1 || plan.Limit > config.MaxResults {
			return []byte(`{"status":"invalid_response","ok":false}`), nil
		}
		return executeStructuredSearch(timed, route.provider, plan.Limit, config.ParserBudget, plan.Body, defaultType, request, cache,
			func(raw []byte) ([]byte, error) { return call(timed, in.Life, raw) })
	})
	return owner, err
}

type structuredResponse struct {
	Attempted []string            `json:"attemptedProviders"`
	Provider  string              `json:"provider"`
	Limit     int                 `json:"limit"`
	Body      *toolresults.Result `json:"body,omitempty"`
	Text      string              `json:"text,omitempty"`
	Partial   bool                `json:"parseLimited"`
	Budget    int                 `json:"parserBudgetBytes"`
	Status    int                 `json:"httpStatus"`
}

type searchBody struct {
	mu      sync.Mutex
	reader  io.ReadCloser
	budget  int
	pending error
	chunk   [32768]byte
	parse   []byte
	partial bool
}

func (s *searchBody) Next(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.pending != nil {
		return nil, s.pending
	}
	n, err := s.reader.Read(s.chunk[:])
	if n == 0 {
		return nil, err
	}
	s.pending = err
	chunk := s.chunk[:n]
	if !s.partial {
		if n <= s.budget-len(s.parse) {
			s.parse = append(s.parse, chunk...)
		} else {
			s.parse, s.partial = nil, true
		}
	}
	return chunk, nil
}

func (s *searchBody) Destroy() { _ = s.reader.Close() }

func executeStructuredSearch(ctx context.Context, provider string, limit, budget int, body []byte, defaultType string,
	requestUpstream func(context.Context, []byte) (*http.Response, error),
	cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error),
	finish func([]byte) ([]byte, error),
) (json.RawMessage, error) {
	observation := structuredResponse{Attempted: []string{provider}, Provider: provider, Limit: limit}
	response, err := requestUpstream(ctx, body)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			contentType := response.Header.Get("Content-Type")
			if contentType == "" {
				contentType = defaultType
			}
			source := &searchBody{reader: response.Body, budget: budget}
			preview := 1024.0
			result, err := cache(ctx, source, toolresults.Options{ContentType: &contentType, Encoding: toolresults.UTF8, PreviewBytes: &preview})
			if err == nil {
				observation.Body, observation.Text, observation.Partial = &result, string(source.parse), source.partial
				observation.Budget, observation.Status = budget, response.StatusCode
			} else {
				// The cache may cancel with an in-flight Next; close the HTTP
				// body before joining, including errors before the iterator ran.
				source.Destroy()
				source.mu.Lock()
				source.mu.Unlock()
			}
		}
	}
	if ctx.Err() != nil {
		return pageError(ctx, ctx.Err()), nil
	}
	request, err := json.Marshal(struct {
		Operation   string             `json:"operation"`
		Observation structuredResponse `json:"observation"`
	}{"search_response", observation})
	if err != nil {
		return nil, err
	}
	return finish(request)
}
