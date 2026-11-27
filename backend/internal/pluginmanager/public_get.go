package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

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

func (m *Manager) startExternalPublicGET(ctx context.Context, in BuiltinRuntimeInput) (BuiltinLifetime, error) {
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
	definitions, err := in.CallGuest(ctx, []byte(`{"operation":"publicGetRegistrations"}`))
	if err != nil {
		return nil, err
	}
	call, err := m.wasm.BindCallWithAdmission(in.ID)
	if err != nil {
		return nil, err
	}
	owner := new(publicGetLifetime)
	fetch := publicpage.Fetcher{Lookup: publicpage.NativeLookup, BeginWork: owner.work.Start}
	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	var active atomic.Int32
	_, err = m.InstallBuiltinTools(in, definitions, func(wait context.Context, _ string, raw json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		var input struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &input); err != nil || input.URL == "" {
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
		response, err := fetch.Get(timed, input.URL)
		if err != nil {
			return pageError(timed, err), nil
		}
		defer response.Body.Finish()
		contentType := response.Header.Get("Content-Type")
		contentEncoding := response.Header.Get("Content-Encoding")
		if contentEncoding == "" {
			contentEncoding = "identity"
		}
		preview := 8192.0 // Existing raw body window, not a response size limit.
		result, err := cache(timed, response.Body, toolresults.Options{ContentType: &contentType, Encoding: "base64", PreviewBytes: &preview})
		if err != nil {
			if timed.Err() != nil {
				return pageError(timed, err), nil
			}
			return []byte(`{"status":"cache_failed","ok":false}`), nil
		}
		request, err := json.Marshal(struct {
			Operation string             `json:"operation"`
			Config    json.RawMessage    `json:"config"`
			Status    int                `json:"httpStatus"`
			URL       string             `json:"sourceUrl"`
			Encoding  string             `json:"contentEncoding"`
			Body      toolresults.Result `json:"body"`
		}{"publicGetResponse", in.Config, response.Status, response.URL, contentEncoding, result})
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
	case errors.Is(err, publicpage.ErrURL):
		return []byte(`{"status":"invalid_url","ok":false}`)
	default:
		return []byte(`{"status":"unavailable","ok":false}`)
	}
}
