package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/httpmedia"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicpage"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

// publicGetLifetime owns DNS relay work which may outlive its logical result.
// The ordinary tool bridge already joins each execution and cache callback.
type publicGetLifetime struct{ work quiescence.Group }

func (o *publicGetLifetime) Dispose() { o.work.Seal() }
func (o *publicGetLifetime) CloseAndWait(ctx context.Context) error {
	o.Dispose()
	return o.work.Wait(ctx)
}

func (m *Manager) startExternalPublicGET(ctx context.Context, in BuiltinRuntimeInput, request bool) (BuiltinLifetime, error) {
	var config struct {
		TimeoutMS      int64 `json:"timeoutMs"`
		MaxConcurrency int32 `json:"maxConcurrency"`
	}
	config.TimeoutMS, config.MaxConcurrency = 60000, 2
	if err := json.Unmarshal(in.Config, &config); err != nil ||
		config.TimeoutMS < 500 || config.TimeoutMS > 2147483647 ||
		config.MaxConcurrency < 1 || config.MaxConcurrency > 64 {
		return nil, errors.New("public_get_configuration_invalid")
	}
	registration := []byte(`{"operation":"publicGetRegistrations"}`)
	if request {
		registration = []byte(`{"operation":"publicRequestRegistrations"}`)
	}
	definitions, err := in.CallGuest(ctx, registration)
	if err != nil {
		return nil, err
	}
	var structuredTool string
	if request {
		var registration struct {
			Tools      json.RawMessage
			Structured string
		}
		if err := json.Unmarshal(definitions, &registration); err != nil || registration.Structured == "" {
			return nil, errors.New("invalid_plugin_registrations")
		}
		definitions, structuredTool = registration.Tools, registration.Structured
	}
	call, err := m.wasm.BindCallWithAdmission(in.ID)
	if err != nil {
		return nil, err
	}
	owner := new(publicGetLifetime)
	fetch := publicpage.Fetcher{Lookup: publicpage.NativeLookup, BeginWork: owner.work.Start}
	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	var active atomic.Int32
	_, err = m.InstallBuiltinTools(in, definitions, func(wait context.Context, tool string, raw json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		var input struct {
			URL string `json:"url"`
		}
		structured := tool == structuredTool
		if !structured && (json.Unmarshal(raw, &input) != nil || input.URL == "") {
			return []byte(`{"status":"invalid_input","ok":false}`), nil
		}
		for {
			n := active.Load()
			if n >= config.MaxConcurrency {
				return []byte(`{"status":"busy","ok":false}`), nil
			}
			if active.CompareAndSwap(n, n+1) {
				break
			}
		}
		defer active.Add(-1)
		timed, cancel := context.WithTimeout(wait, timeout)
		defer cancel()
		var response publicpage.Response
		var err error
		if structured {
			response, err = fetch.Request(timed, raw)
		} else {
			response, err = fetch.Get(timed, input.URL)
		}
		if err != nil {
			return pageError(timed, err), nil
		}
		defer response.Body.Finish()
		contentType := response.Header.Get("Content-Type")
		wireEncoding := response.Header.Get("Content-Encoding")
		contentEncoding := wireEncoding
		if contentEncoding == "" {
			contentEncoding = "identity"
		}
		operation, encoding := "publicGetResponse", toolresults.Base64
		if structured {
			operation = "publicRequestResponse"
			if wireEncoding == "" && httpmedia.Textual(contentType) {
				encoding = toolresults.UTF8
			}
		}
		preview := 8192.0 // Existing raw body window, not a response size limit.
		result, err := cache(timed, response.Body, toolresults.Options{ContentType: &contentType, Encoding: encoding, PreviewBytes: &preview})
		if err != nil {
			if timed.Err() != nil {
				return pageError(timed, err), nil
			}
			return []byte(`{"status":"cache_failed","ok":false}`), nil
		}
		request, err := json.Marshal(struct {
			Operation  string             `json:"operation"`
			Config     json.RawMessage    `json:"config"`
			Status     int                `json:"httpStatus"`
			URL        string             `json:"sourceUrl"`
			Encoding   string             `json:"contentEncoding"`
			Body       toolresults.Result `json:"body"`
			RawHeaders []string           `json:"rawHeaders,omitempty"`
		}{operation, in.Config, response.Status, response.URL, contentEncoding, result, response.RawHeaders})
		if err != nil {
			return nil, err
		}
		return call(timed, in.Life, request)
	})
	return owner, err
}

func pageError(ctx context.Context, err error) json.RawMessage {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return []byte(`{"status":"timeout","ok":false}`)
	case ctx.Err() != nil:
		return []byte(`{"status":"cancelled","ok":false}`)
	case errors.Is(err, publicpage.ErrPrivate):
		return []byte(`{"status":"address_not_public","ok":false}`)
	case errors.Is(err, publicpage.ErrRedirect):
		return []byte(`{"status":"redirect_not_supported","ok":false}`)
	case errors.Is(err, publicpage.ErrHeader):
		return []byte(`{"status":"invalid_header","ok":false}`)
	case errors.Is(err, publicpage.ErrInput):
		return []byte(`{"status":"invalid_input","ok":false}`)
	case errors.Is(err, publicpage.ErrURL):
		return []byte(`{"status":"invalid_url","ok":false}`)
	default:
		return []byte(`{"status":"unavailable","ok":false}`)
	}
}
