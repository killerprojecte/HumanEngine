// Package toolresults implements the ephemeral, owner-scoped disk cache from
// src/tool-results.ts. It stores complete streams and budgets only model views.
package toolresults

import (
	"context"
	"errors"
	"io"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const (
	ViewBytes           = 32768
	DefaultPreviewBytes = 4096
	MaxSafeInteger      = 9007199254740991
	Retention           = "Until expiry, source invalidation, plugin disable, or runtime shutdown; no restart persistence."
)

var (
	ErrCacheClosed  = errors.New("tool_result_cache_closed")
	ErrCancelled    = errors.New("tool_result_cancelled")
	ErrStorage      = errors.New("tool_result_storage_failed")
	ErrInvalidated  = errors.New("tool_result_invalidated")
	ErrInvalidRange = errors.New("tool_result_invalid_range")
	ErrNotFound     = errors.New("tool_result_not_found_or_expired")
	ErrExpired      = errors.New("tool_result_expired")
)

type Encoding string

const (
	UTF8   Encoding = "utf8"
	Base64 Encoding = "base64"
)

// Source transfers ownership of each chunk until the next Next call. io.EOF
// means done; an empty chunk is not EOF. A non-nil error means no chunk, so an
// io.Reader adapter must deliver its final bytes before delivering io.EOF.
// Next calls are serialized. Cancellation can return Store while one Next is
// still running; that call owns no cache state and its eventual result is ignored.
// Implementations should observe ctx or implement Destroyer to release resources.
type Source interface {
	Next(context.Context) ([]byte, error)
}

// Destroyer is the source stream's immediate socket/resource release hook. It
// MUST be nonblocking, safe concurrently with Next, and idempotent: like Node,
// an incomplete failing stream can be destroyed both by write and store cleanup.
type Destroyer interface{ Destroy() }

// Returner corresponds to an iterator's return(). It is invoked asynchronously
// on incomplete iteration, may overlap the stalled Next, and is never awaited.
// It must not access caller-owned state whose lifetime ends with Store.
type Returner interface{ Return() error }

type NextFunc func(context.Context) ([]byte, error)

func (f NextFunc) Next(ctx context.Context) ([]byte, error) { return f(ctx) }

// Bytes makes a one-chunk source. The caller must not mutate data during Store.
func Bytes(data []byte) Source { return &single{data: data} }

// Text accepts the canonical WTF-8 strings used by jsjson. Like Buffer.from,
// it encodes unpaired UTF-16 surrogates as U+FFFD at the storage boundary.
func Text(value string) Source {
	text, err := jsjson.UTF8Text(value)
	return &single{data: []byte(text), err: err, text: true}
}

type single struct {
	data []byte
	err  error
	text bool
	done bool
}

func (s *single) Next(context.Context) ([]byte, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return s.data, s.err
}

// Validation returns a live source guard. An error corresponds to a thrown
// source callback exception; false specifically means source invalidation.
// Guards and adoption run without the cache mutex, may reenter the cache, and
// must be concurrency-safe when callers perform concurrent cache operations.
type Validation func() (bool, error)
type Owner struct {
	// Generation is native-only and opt-in; legacy owners retain their old contract.
	Generation *Generation `json:"-"`
	// Retained is a native-only immutable adoption capability. It is never serialized.
	Retained     any
	Scope        string
	SceneID      string
	PluginID     string
	Dependencies []string
	Validations  []Validation
}

func (o Owner) clone() Owner {
	o.Dependencies = append([]string(nil), o.Dependencies...)
	o.Validations = append([]Validation(nil), o.Validations...)
	return o
}
func (o Owner) validate() (bool, error) {
	if !o.generationCurrent() {
		return false, nil
	}
	for _, check := range o.Validations {
		valid, err := check()
		if err != nil || !valid {
			return valid, err
		}
	}
	return o.generationCurrent(), nil
}

type Options struct {
	ContentType  *string
	Encoding     Encoding
	PreviewBytes *float64
}

// ReadInput retains Number-valued ranges until validation. This rejects NaN,
// infinities, fractional/negative values, and numbers outside the existing JS
// safe-integer contract instead of silently rounding them in a transport adapter.
type ReadInput struct {
	ResultID string
	Offset   float64
	Length   *float64
	Full     bool
	Encoding Encoding
}

type Range struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}
type ReturnedRange struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
	End    int64 `json:"end"`
}
type Result struct {
	ResultID        string        `json:"resultId"`
	TotalBytes      int64         `json:"totalBytes"`
	ContentType     string        `json:"contentType"`
	Complete        bool          `json:"complete"`
	ExpiresAt       string        `json:"expiresAt"`
	Retention       string        `json:"retention"`
	RangeUnit       string        `json:"rangeUnit"`
	RequestedRange  Range         `json:"requestedRange"`
	ReturnedRange   ReturnedRange `json:"returnedRange"`
	Encoding        Encoding      `json:"encoding"`
	Content         string        `json:"content"`
	Truncated       bool          `json:"truncated"`
	RequestComplete bool          `json:"requestComplete"`
	NextOffset      *int64        `json:"nextOffset"`
	ViewBudgetBytes int           `json:"viewBudgetBytes"`
	RetrievalTool   string        `json:"retrievalTool"`
	Trust           string        `json:"trust"`
}

// JSONValue preserves the source's property order and lone UTF-16 surrogates in
// contentType. Use this with jsjson, not encoding/json at the JS wire boundary.
func (r Result) JSONValue() (*jsjson.Object, error) {
	requested, err := jsjson.NewObject(jsjson.Member{Key: "offset", Value: float64(r.RequestedRange.Offset)}, jsjson.Member{Key: "length", Value: float64(r.RequestedRange.Length)})
	if err != nil {
		return nil, err
	}
	returned, err := jsjson.NewObject(jsjson.Member{Key: "offset", Value: float64(r.ReturnedRange.Offset)}, jsjson.Member{Key: "length", Value: float64(r.ReturnedRange.Length)}, jsjson.Member{Key: "end", Value: float64(r.ReturnedRange.End)})
	if err != nil {
		return nil, err
	}
	var next any
	if r.NextOffset != nil {
		next = float64(*r.NextOffset)
	}
	return jsjson.NewObject(
		jsjson.Member{Key: "resultId", Value: r.ResultID}, jsjson.Member{Key: "totalBytes", Value: float64(r.TotalBytes)},
		jsjson.Member{Key: "contentType", Value: r.ContentType}, jsjson.Member{Key: "complete", Value: r.Complete},
		jsjson.Member{Key: "expiresAt", Value: r.ExpiresAt}, jsjson.Member{Key: "retention", Value: r.Retention},
		jsjson.Member{Key: "rangeUnit", Value: r.RangeUnit}, jsjson.Member{Key: "requestedRange", Value: requested},
		jsjson.Member{Key: "returnedRange", Value: returned}, jsjson.Member{Key: "encoding", Value: string(r.Encoding)},
		jsjson.Member{Key: "content", Value: r.Content}, jsjson.Member{Key: "truncated", Value: r.Truncated},
		jsjson.Member{Key: "requestComplete", Value: r.RequestComplete}, jsjson.Member{Key: "nextOffset", Value: next},
		jsjson.Member{Key: "viewBudgetBytes", Value: float64(r.ViewBudgetBytes)}, jsjson.Member{Key: "retrievalTool", Value: r.RetrievalTool},
		jsjson.Member{Key: "trust", Value: r.Trust},
	)
}
