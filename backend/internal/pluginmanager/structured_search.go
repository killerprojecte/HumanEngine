package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"golang.org/x/net/http/httpguts"
)

func (m *Manager) startStructuredSearch(ctx context.Context, in BuiltinRuntimeInput, profile structuredProfile) (BuiltinLifetime, error) {
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
	value, err := jsjson.Parse(string(in.Config))
	if err != nil {
		return nil, NewStartupError("invalid_plugin_configuration", nil)
	}
	settings := value.(*jsjson.Object)
	var owners structuredLifetime
	var attempts []structuredAttempt
	keys := map[string]string{}
	for _, route := range profile.routesFor(settings) {
		attempt := structuredAttempt{provider: route.provider}
		if route.consent != "" && prop(settings, route.consent) != true {
			attempts = append(attempts, attempt)
			continue
		}
		var key string
		if route.consent == "" {
			var cached bool
			key, cached = keys[route.credential]
			if !cached {
				var found bool
				key, found, err = in.Secret(ctx, route.credential)
				if err != nil {
					return nil, NewStartupError(err.Error(), nil)
				}
				if !found || !httpguts.ValidHeaderFieldValue(key) {
					key = ""
				}
				keys[route.credential] = key
			}
			if key == "" {
				if !profile.combined {
					return nil, NewStartupError("credential_not_configured", nil)
				}
				attempts = append(attempts, attempt)
				continue
			}
		}
		owner := newStructuredTransport(in.Life, route.endpoint, route.authHeader, key)
		owners = append(owners, owner)
		attempt.request, attempt.contentType = owner.post, "application/json"
		if route.consent != "" {
			if route.queryField != "" {
				owner.queryField = route.queryField
			}
			attempt.request, attempt.contentType = owner.get, "text/html"
		}
		attempts = append(attempts, attempt)
	}
	definitions, err := in.CallGuest(ctx, []byte(`{"operation":"search_registrations"}`))
	if err != nil {
		return nil, err
	}
	call, err := m.wasm.BindCallWithAdmission(in.ID)
	if err != nil {
		return nil, err
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
		return runStructuredSearch(timed, attempts, config.MaxResults, config.ParserBudget, raw, cache,
			func(raw []byte) ([]byte, error) { return call(timed, in.Life, raw) })

	})
	return owners, err
}

type structuredResponse struct {
	Attempted []string            `json:"attemptedProviders"`
	Provider  string              `json:"provider"`
	Limit     int                 `json:"limit"`
	Body      *toolresults.Result `json:"body,omitempty"`
	Text      string              `json:"text,omitempty"`
	Retained  bool                `json:"retained,omitempty"`
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

type structuredLifetime []*structuredTransport

func (s structuredLifetime) Dispose() {
	for _, owner := range s {
		owner.Dispose()
	}
}

func (s structuredLifetime) CloseAndWait(ctx context.Context) error {
	s.Dispose()
	var err error
	for _, owner := range s {
		err = errors.Join(err, owner.dials.Wait(ctx))
	}
	return err
}

type structuredAttempt struct {
	provider    string
	contentType string
	request     func(context.Context, []byte) (*http.Response, error)
}

type structuredCache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)

var invalidSearch = json.RawMessage(`{"status":"invalid_response","ok":false}`)

func runStructuredSearch(ctx context.Context, attempts []structuredAttempt, maxResults, budget int, input json.RawMessage,
	cache structuredCache, guest func([]byte) ([]byte, error),
) (json.RawMessage, error) {
	attempted := make([]string, 0, len(attempts))
	var prior structuredResponse
	for i, attempt := range attempts {
		if ctx.Err() != nil {
			return pageError(ctx, ctx.Err()), nil
		}
		planInput, err := json.Marshal(struct {
			Operation  string          `json:"operation"`
			Provider   string          `json:"provider"`
			MaxResults int             `json:"maxResults"`
			Input      json.RawMessage `json:"input"`
		}{"search_plan", attempt.provider, maxResults, input})
		if err != nil {
			return nil, err
		}
		reply, err := guest(planInput)
		if err != nil {
			return nil, err
		}
		var plan struct {
			Limit int             `json:"limit"`
			Body  json.RawMessage `json:"body"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(reply, &plan); err != nil || plan.Error != "" || len(plan.Body) == 0 ||
			plan.Limit < 1 || plan.Limit > maxResults {
			return invalidSearch, nil
		}

		attempted = append(attempted, attempt.provider)
		observation, cacheFailed := attempt.observe(ctx, plan.Limit, budget, plan.Body, cache)
		if ctx.Err() != nil {
			return pageError(ctx, ctx.Err()), nil
		}
		hasFallback := i+1 < len(attempts) && !cacheFailed
		if observation.Body == nil {
			if hasFallback {
				continue
			}
			observation = prior
		}
		observation.Attempted = attempted
		result, err := finishStructuredSearch(observation, guest)
		if err != nil || !hasFallback {
			return result, err
		}
		var verdict struct{ Status string }
		if err := json.Unmarshal(result, &verdict); err != nil || verdict.Status != "unavailable" {
			return result, err
		}
		// Only a parser failure may reach here with a cache reference. Keep
		// its provenance if the next provider does not cache another body.
		prior = observation
		prior.Text, prior.Retained = "", true
	}
	return invalidSearch, nil
}

func finishStructuredSearch(observation structuredResponse, finish func([]byte) ([]byte, error)) (json.RawMessage, error) {
	request, err := json.Marshal(struct {
		Operation   string             `json:"operation"`
		Observation structuredResponse `json:"observation"`
	}{"search_response", observation})
	if err != nil {
		return nil, err
	}
	return finish(request)
}

// The cache outcome is private: public "unavailable" alone cannot authorize
// another provider request after a local cache or stream failure.
func (a structuredAttempt) observe(ctx context.Context, limit, budget int, body []byte, cache structuredCache) (structuredResponse, bool) {
	observation := structuredResponse{Provider: a.provider, Limit: limit}
	if a.request == nil {
		return observation, false
	}
	response, err := a.request(ctx, body)
	if err != nil {
		return observation, false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return observation, false
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = a.contentType
	}
	source := &searchBody{reader: response.Body, budget: budget}
	preview := 1024.0
	result, err := cache(ctx, source, toolresults.Options{ContentType: &contentType, Encoding: toolresults.UTF8, PreviewBytes: &preview})
	if err != nil {
		// The cache can stop with an in-flight Next. Close before joining.
		source.Destroy()
		source.mu.Lock()
		source.mu.Unlock()
		return observation, true
	}
	observation.Body, observation.Text, observation.Partial = &result, string(source.parse), source.partial
	observation.Budget, observation.Status = budget, response.StatusCode
	return observation, false
}
