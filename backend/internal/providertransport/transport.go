// Package providertransport owns native HTTP transmission, not global startup.
package providertransport

import (
	"bytes"
	"context"
	"errors"
	"golang.org/x/text/encoding/unicode"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

type Provider struct {
	ImageUploadLimits *ImageUploadLimits

	ID, Protocol, BaseURL, CredentialEnv string
	CredentialConfigured                 bool
}
type Descriptor struct {
	ProviderID, ModelID, Protocol, APIModelName, Operation, Role string
	Prices                                                       map[string]float64
	OpaqueInput                                                  bool
}

// BudgetAttempt is an admitted durable reservation. All transitions report failures.
type BudgetAttempt interface {
	Body() map[string]any
	Start(context.Context) error
	Settle(context.Context, map[string]any) error
	Fail(context.Context) error
}
type BudgetGate interface {
	Prepare(context.Context, Descriptor, map[string]any) (BudgetAttempt, error)
}
type BudgetSource interface {
	Current(context.Context) (BudgetGate, error)
}

// ExplicitOff is only appropriate for a verified disabled budget configuration.
// It is not a substitute for an unavailable or restoring budget owner.
type ExplicitOff struct{}

func (ExplicitOff) Current(context.Context) (BudgetGate, error) { return nil, nil }

type CredentialResolver interface {
	Resolve(context.Context, Provider) (string, error)
}
type StoredCredentials struct {
	Store     *credentials.Store
	LookupEnv func(string) (string, bool)
}

func (c StoredCredentials) Resolve(ctx context.Context, p Provider) (string, error) {
	if c.Store == nil {
		return "", errors.New("credential_store_required")
	}
	secret, ok, err := c.Store.Get(ctx, "model", p.ID)
	if err != nil {
		return "", err
	}
	if ok {
		return secret, nil
	}
	lookup := c.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if p.CredentialEnv != "" {
		v, _ := lookup(p.CredentialEnv)
		return v, nil
	}
	return "", nil
}

// ImageSerializer must honor source compression and animation policy.
type ImageSerializer interface {
	Serialize(context.Context, map[string]any, *ImageUploadLimits, *int) ([]byte, error)
}
type Client struct {
	HTTP            *http.Client
	Credentials     CredentialResolver
	Budgets         BudgetSource
	Images          ImageSerializer
	MaxOutputTokens *int
	Counter         CounterRegistry
}
type Request struct {
	credentialUsed    func(string)
	diagnostic        func(context.Context, *Completion, []string)
	diagnosticSecrets []string
	ImageRequestLimit *int

	Provider     Provider
	Path         string
	Body         map[string]any
	Descriptor   *Descriptor
	FirstContent func()
	Measurement  func(Measurement)
}

func safe(code string) *modelservice.ModelError { return modelservice.NewError(code, nil, false) }
func budgetFailure(err error) error {
	code := err.Error()
	if !modelservice.IsBudgetFailureCode(code) {
		switch code {
		case "invalid_budget_category", "invalid_budget_integer", "invalid_budget_price", "invalid_budget_estimate", "invalid_budget_identity", "invalid_budget_metadata", "invalid_budget_mode", "invalid_budget_request":
			code = "budget_configuration_invalid"
		default:
			code = "budget_admission_failed"
		}
	}
	return safe(code)
}
func inlineImages(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, slot := range imageSlots(m) {
		if strings.HasPrefix(slot.get(), "data:image/") {
			return true
		}
	}
	return false
}
func (c *Client) Request(ctx context.Context, r Request) (result map[string]any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, safe("request_cancelled")
	}
	if c.MaxOutputTokens != nil && (*c.MaxOutputTokens < 1 || *c.MaxOutputTokens > 1000000) {
		return nil, safe("invalid_output_cap")
	}

	base, e := providerBaseURL(r.Provider.BaseURL)
	if e != nil {
		return nil, e
	}
	if c.Credentials == nil {
		return nil, safe("credential_resolver_required")
	}
	credential, e := c.Credentials.Resolve(ctx, r.Provider)
	if e != nil {
		return nil, safe("provider_request_failed")
	}
	if r.credentialUsed != nil {
		r.credentialUsed(credential)
	}
	if credential == "" && (r.Provider.CredentialEnv != "" || r.Provider.CredentialConfigured) {
		return nil, safe("credential_not_configured")
	}
	if c.Budgets == nil {
		return nil, safe("budget_owner_required")
	}
	httpClient := http.Client{}
	if c.HTTP != nil {
		httpClient = *c.HTTP
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect_disallowed") }
	body := r.Body
	streaming := body != nil && body["stream"] == true
	first := false
	var attempt BudgetAttempt
	// Accounting outlives caller cancellation; failed durable transitions never become success.
	accounting := context.WithoutCancel(ctx)
	defer func() {
		if err == nil {
			return
		}
		if attempt != nil {
			if failure := attempt.Fail(accounting); failure != nil {
				err = budgetFailure(failure)
				return
			}
		}
		if failure, ok := err.(*modelservice.ModelError); ok && modelservice.IsBudgetFailureCode(failure.Code) {
			return
		}
		if ctx.Err() != nil {
			if cause, ok := context.Cause(ctx).(*modelservice.ModelError); ok {
				err = safe(cause.Code)
			} else {
				err = safe("request_cancelled")
			}
			return
		}
		var stream *providerwire.StreamError
		if errors.As(err, &stream) || first {
			code := "provider_stream_invalid"
			if first {
				code = "provider_stream_interrupted"
			}
			err = safe(code)
		}
	}()
	requestLimit := r.ImageRequestLimit
	for {
		gate, e := c.Budgets.Current(ctx)
		if e != nil {
			return nil, budgetFailure(e)
		}
		if gate != nil && r.Descriptor != nil {
			attempt, e = gate.Prepare(ctx, *r.Descriptor, body)
			if e != nil {
				return nil, budgetFailure(e)
			}
			body = attempt.Body()
		}
		if body != nil && r.Descriptor != nil && r.Descriptor.Operation == "complete" && c.MaxOutputTokens != nil {
			key := "max_completion_tokens"
			if r.Provider.Protocol == "openai-responses" {
				key = "max_output_tokens"
			}
			copyBody := map[string]any{}
			for k, v := range body {
				copyBody[k] = v
			}
			cap := float64(*c.MaxOutputTokens)
			if old, ok := body[key].(float64); ok && old < cap {
				cap = old
			}
			copyBody[key] = cap
			body = copyBody
		}
		var serialized []byte
		if body != nil {
			if inlineImages(body) || r.Provider.ImageUploadLimits != nil {
				images := c.Images
				if images == nil {
					images = NativeImageSerializer{}
				}
				serialized, e = images.Serialize(ctx, body, r.Provider.ImageUploadLimits, requestLimit)
			} else {
				serialized, e = providerwire.EncodeJSON(body)
			}
			if e != nil {
				return nil, safe("provider_request_failed")
			}
		}
		var measurement *Measurement
		if serialized != nil && r.Descriptor != nil {
			m := Measure(serialized, *r.Descriptor, "native-json:"+r.Path+":v1", c.Counter)
			measurement = &m
		}
		if ctx.Err() != nil {
			return nil, safe("request_cancelled")
		}
		finalGate, e := c.Budgets.Current(ctx)
		if e != nil {
			return nil, budgetFailure(e)
		}
		if r.Descriptor != nil && attempt == nil && finalGate != nil {
			return nil, safe("budget_configuration_changed")
		}
		if attempt != nil {
			if e = attempt.Start(accounting); e != nil {
				return nil, budgetFailure(e)
			}
		}
		method := http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
		req, e := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+"/"+r.Path, bytes.NewReader(serialized))
		if e != nil {
			return nil, safe("provider_request_failed")
		}
		accept := "application/json"
		if streaming {
			accept = "text/event-stream, application/json"
		}
		req.Header.Set("Accept", accept)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if measurement != nil && r.Measurement != nil {
			func() { defer func() { _ = recover() }(); r.Measurement(*measurement) }()
		}
		requestStarted := time.Now()
		response, e := httpClient.Do(req)
		if e != nil {
			return nil, networkFailure(e)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			if response.StatusCode == 413 && serialized != nil && inlineImages(body) {
				if attempt != nil {
					if e = attempt.Fail(accounting); e != nil {
						return nil, budgetFailure(e)
					}
					attempt = nil
				}
				limit := len(serialized) / 2
				next := r
				next.Body = body
				next.ImageRequestLimit = &limit
				return nil, &imageRetry{request: next}
			}
			return nil, modelservice.HTTPFailure(response.StatusCode, retryAfter(response.Header.Get("Retry-After")))
		}
		contentType := response.Header.Get("Content-Type")
		if streaming && strings.Contains(contentType, "text/event-stream") {
			result, e = providerwire.ReadStream(response.Body, r.Provider.Protocol, func(kind string) {
				modelservice.ObserveFirstContent(ctx, kind, float64(time.Since(requestStarted))/float64(time.Millisecond))
				first = true
				if r.FirstContent != nil {
					r.FirstContent()
				}
			})
			if e != nil {
				return nil, e
			}
		} else {
			if streaming {
				modelservice.ObserveTransportMode(ctx, "non_stream")
			}
			if !strings.Contains(contentType, "application/json") {
				_ = response.Body.Close()
				return nil, safe("invalid_provider_content_type")
			}
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
			_ = response.Body.Close()
			if readErr != nil {
				return nil, networkFailure(readErr)
			}
			if len(raw) > 8*1024*1024 {
				return nil, safe("provider_response_too_large")
			}
			if attempt == nil && ctx.Err() != nil {
				return nil, safe("request_cancelled")
			}
			decoded, _ := unicode.UTF8.NewDecoder().Bytes(raw)
			value, decodeErr := providerwire.DecodeJSON(decoded)
			if decodeErr != nil {
				return nil, safe("invalid_provider_json")
			}
			var ok bool
			result, ok = value.(map[string]any)
			if !ok {
				return nil, safe("invalid_provider_response")
			}
		}
		if attempt != nil {
			usage := providerwire.NormalizeProviderUsage(result["usage"], "openai", r.Path == "embeddings")
			if e = attempt.Settle(accounting, usage); e != nil {
				return nil, budgetFailure(e)
			}
		}
		if ctx.Err() != nil {
			return nil, safe("request_cancelled")
		}
		return result, nil
	}
}
func retryAfter(value string) *float64 {
	return modelservice.ParseRetryAfter(&value, float64(time.Now().UnixMilli()))
}
func networkFailure(err error) error {
	code := ""
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		code = "ECONNRESET"
	case errors.Is(err, syscall.ECONNREFUSED):
		code = "ECONNREFUSED"
	case errors.Is(err, syscall.ETIMEDOUT):
		code = "ETIMEDOUT"
	case errors.Is(err, syscall.ENETUNREACH):
		code = "ENETUNREACH"
	case errors.Is(err, syscall.EHOSTUNREACH):
		code = "EHOSTUNREACH"
	case errors.Is(err, syscall.EPIPE):
		code = "EPIPE"
	default:
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsTemporary {
			code = "EAI_AGAIN"
		}
	}
	return modelservice.BuiltinNetworkFailure(code, "", nil)
}

type imageRetry struct{ request Request }

func (*imageRetry) Error() string { return "native_image_retry" }
