// Package modelservice executes the routing and attempt policy of src/models.ts.
// It is a policy engine, not a production provider, billing, or recovery adapter.
package modelservice

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Provider struct {
	ID         string   `json:"id"`
	Protocol   string   `json:"protocol"`
	RetryCount *float64 `json:"retryCount,omitempty"`
	TimeoutMS  *float64 `json:"timeoutMs,omitempty"`
}
type Model struct {
	ID           string   `json:"id"`
	ProviderID   string   `json:"providerId"`
	APIModelName string   `json:"apiModelName"`
	Capabilities []string `json:"capabilities"`
	Streaming    *bool    `json:"streaming,omitempty"`
}
type Route struct {
	Role            string   `json:"role"`
	OrderedModelIDs []string `json:"orderedModelIds"`
	TimeoutMS       *float64 `json:"timeoutMs,omitempty"`
}
type Config struct {
	NativeMaxOutputTokens *float64   `json:"nativeMaxOutputTokens,omitempty"`
	Providers             []Provider `json:"providers"`
	Models                []Model    `json:"models"`
	Routes                []Route    `json:"routes"`
}
type Request struct {
	Role      string   `json:"role"`
	ToolNames []string `json:"toolNames"`
	HasImages bool     `json:"hasImages"`
}
type Candidate struct {
	Role      string
	Index     int // One-based index in the original route, including skipped entries.
	Remaining int
	Model     Model
	Provider  Provider
}
type Selection struct {
	ModelID    string     `json:"modelId"`
	Index      int        `json:"index"`
	SkipReason string     `json:"skipReason,omitempty"`
	Candidate  *Candidate `json:"-"`
}
type RequestPolicy struct {
	RetryCount  int     `json:"retryCount"`
	MaxAttempts int     `json:"maxAttempts"`
	TimeoutMS   float64 `json:"timeoutMs"`
}

func CanonicalRole(role string) string {
	if role == "thinker" {
		return "replyer"
	}
	return role
}
func IsBuiltin(protocol string) bool {
	return protocol == "openai-chat" || protocol == "openai-responses"
}
func BasePriority(role string) float64 {
	switch CanonicalRole(role) {
	case "planner", "replyer", "vision":
		return 0
	default:
		return 1
	}
}
func ProviderPolicy(provider Provider) (RequestPolicy, error) {
	retry, timeout := 2.0, 30000.0
	if provider.RetryCount != nil {
		retry = *provider.RetryCount
	}
	if provider.TimeoutMS != nil {
		timeout = *provider.TimeoutMS
	}
	if math.IsNaN(retry) || math.IsInf(retry, 0) || math.Trunc(retry) != retry || retry < 0 || retry > 5 || math.IsNaN(timeout) || math.IsInf(timeout, 0) || math.Trunc(timeout) != timeout || timeout < 100 || timeout > 300000 {
		return RequestPolicy{}, errors.New("invalid_provider_request_policy")
	}
	return RequestPolicy{int(retry), int(retry) + 1, timeout}, nil
}
func ProviderOperationBudget(provider Provider) (float64, error) {
	policy, err := ProviderPolicy(provider)
	if err != nil {
		return 0, err
	}
	return float64(policy.MaxAttempts)*policy.TimeoutMS + float64(policy.RetryCount)*10000, nil
}
func RetryDelay(attempt float64, retryAfterMS *float64) float64 {
	exponent := 6.0
	if !math.IsNaN(attempt) && !math.IsInf(attempt, 0) {
		exponent = math.Min(6, math.Max(0, math.Floor(attempt)-1))
	}
	requested := 0.0
	if retryAfterMS != nil && !math.IsNaN(*retryAfterMS) {
		requested = math.Min(10000, math.Max(0, *retryAfterMS))
	}
	return math.Max(math.Min(10000, 250*math.Pow(2, exponent)), requested)
}

var retrySeconds = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$`)

// ParseRetryAfter handles numeric seconds and HTTP dates. DateParser may supply
// the source runtime's broader Date.parse grammar; an omitted parser uses the
// three HTTP-date forms specified for Retry-After. Never pass a body or URL here.
func ParseRetryAfter(value *string, nowMS float64, dateParser ...func(string) (float64, bool)) *float64 {
	if value == nil {
		return nil
	}
	text := trimECMAScriptSpace(*value)
	if text == "" {
		return nil
	}
	var duration float64
	if retrySeconds.MatchString(text) {
		n, _ := strconv.ParseFloat(text, 64)
		duration = n * 1000
	} else if len(dateParser) > 0 && dateParser[0] != nil {
		date, ok := dateParser[0](text)
		if !ok {
			return nil
		}
		duration = date - nowMS
	} else {
		date, err := http.ParseTime(text)
		if err != nil {
			return nil
		}
		// The source uses Date.parse, whose legacy two-digit year pivot is 50.
		// Go's RFC850 parser uses 69. Keep valid HTTP-date input in parity while
		// leaving the broader JavaScript grammar to the explicit parser hook.
		if legacy, legacyErr := time.Parse(time.RFC850, text); legacyErr == nil && legacy.Year() >= 2050 && legacy.Year() <= 2068 {
			date = legacy.AddDate(-100, 0, 0)
		}
		duration = float64(date.UnixMilli()) - nowMS
	}
	if math.IsNaN(duration) {
		return nil
	}
	result := math.Min(10000, math.Max(0, duration))
	return &result
}

// String.trim uses ECMAScript WhiteSpace and LineTerminator characters, not
// Go's Unicode White_Space property (which includes U+0085 but omits U+FEFF).
func trimECMAScriptSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch r {
		case '\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020', '\u00A0', '\u1680', '\u2028', '\u2029', '\u202F', '\u205F', '\u3000', '\uFEFF':
			return true
		}
		return r >= '\u2000' && r <= '\u200A'
	})
}
func copyNumber(n *float64) *float64 {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}
func cloneModel(m Model) Model {
	m.Capabilities = append([]string(nil), m.Capabilities...)
	if m.Streaming != nil {
		v := *m.Streaming
		m.Streaming = &v
	}
	return m
}
func cloneProvider(p Provider) Provider {
	p.RetryCount = copyNumber(p.RetryCount)
	p.TimeoutMS = copyNumber(p.TimeoutMS)
	return p
}
func cloneCandidate(c Candidate) Candidate {
	c.Model = cloneModel(c.Model)
	c.Provider = cloneProvider(c.Provider)
	return c
}
func contains(items []string, target string) bool {
	for _, v := range items {
		if v == target {
			return true
		}
	}
	return false
}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidateToolNames(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		wire := strings.ReplaceAll(name, ".", "__")
		if !toolName.MatchString(wire) || seen[wire] {
			return NewError("invalid_tool_names", nil, false)
		}
		seen[wire] = true
	}
	return nil
}

// WallClock preserves Node setTimeout coercion rather than adding a deadline cap.
type WallClock struct{}

func (WallClock) Now() float64 { return float64(time.Now().UnixMilli()) }
func (WallClock) AfterFunc(ms float64, f func()) Timer {
	if math.IsNaN(ms) || ms < 1 || ms > 2147483647 {
		ms = 1
	}
	return time.AfterFunc(time.Duration(math.Trunc(ms))*time.Millisecond, f)
}
