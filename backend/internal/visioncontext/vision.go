// Package visioncontext implements ephemeral image reply context. It never persists observations.
package visioncontext

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"regexp"
	"sort"
	"sync"
	"time"
)

type Message struct {
	ID, SceneID, Status, SentAt string
	Parts                       []Part
	Source                      any
}
type Part struct {
	Type, MediaType, AssetID string
	Index                    int
	Reference                any
}
type Candidate struct {
	Message *Message
	Part    Part
}
type Observation struct {
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason,omitempty"`
	FramePolicy string `json:"framePolicy,omitempty"`
}
type Image struct {
	MessageID string `json:"messageId"`
	Observation
}
type Omitted struct {
	MessageID string `json:"messageId"`
	Reason    string `json:"reason"`
}
type Result struct {
	Trust       string    `json:"trust"`
	FramePolicy string    `json:"framePolicy"`
	Images      []Image   `json:"images"`
	Omitted     []Omitted `json:"omitted"`
}

// Library functions preserve undefined versus a matched empty string with the bool return.
type Library struct {
	Lookup              func(Candidate) (string, bool, error)
	AddLookupValidation func(Candidate, string)
	CanRecognize        func(Candidate) (bool, error)
	Recognize           func(context.Context, []byte, Candidate) (string, bool, error)
}

// Ports are bound to the real turn authority by the composition owner. Complete
// must release its provider completion ownership after copying the returned text.
type Ports struct {
	Recalled            func(sceneID, messageID string) (bool, error)
	SourceAvailable     func(Candidate) (bool, error)
	ReadImage           func(context.Context, Candidate) ([]byte, error)
	ImageDataURL        func(context.Context, []byte) (string, error)
	Complete            func(context.Context, []ChatMessage) (string, error)
	AddDependency       func(string)
	AddSourceValidation func(Candidate)
	// ReportError receives the original error for internal diagnostics only. It
	// must not log image bytes, descriptions or unsanitized external messages.
	ReportError func(operation string, err error)
}
type ChatMessage struct {
	Role, Content string
	Images        []string
}
type Config struct {
	SceneID, Mode, Timezone string
	Models                  any
	ReadModels              func() (*jsjson.Object, error)
	ReadTimezone            func() (string, error)
	VisionRoute             bool
	PriorityIDs             []string
	Now                     func() time.Time
}

// Turn must be owned by one runtime turn and discarded on its release. The mutex
// makes repeated/concurrent calls share the same four-image budget and results.
type Turn struct {
	mu      sync.Mutex
	budget  map[string]bool
	results map[string]Observation
}
type entry struct {
	key, scene, message, description string
	expires                          time.Time
}
type Cache struct {
	mu      sync.Mutex
	entries []entry
	Now     func() time.Time
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.entries {
		if e.key == key {
			if e.expires.After(c.now()) {
				return e.description, true
			}
			c.entries = append(c.entries[:i], c.entries[i+1:]...)
			break
		}
	}
	return "", false
}
func (c *Cache) Put(key, scene, message, description string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(func(e entry) bool { return e.key == key })
	c.entries = append(c.entries, entry{key, scene, message, description, c.now().Add(5 * time.Minute)})
	if len(c.entries) > 64 {
		c.entries = c.entries[len(c.entries)-64:]
	}
}
func (c *Cache) remove(f func(entry) bool) {
	out := c.entries[:0]
	for _, e := range c.entries {
		if !f(e) {
			out = append(out, e)
		}
	}
	c.entries = out
}
func (c *Cache) Invalidate(scene, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(func(e entry) bool { return e.scene == scene && e.message == message })
}
func (c *Cache) Clear() { c.mu.Lock(); defer c.mu.Unlock(); c.entries = nil }
func hash(values ...any) (string, error) {
	s, e := jsjson.Stringify(values)
	if e != nil {
		return "", e
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s))), nil
}
func pair(c Candidate) string {
	s, _ := jsjson.Stringify([]any{c.Message.ID, c.Part.AssetID})
	return s
}
func trimSlice(s string) (string, error) {
	u, e := jsjson.UTF16(s)
	if e != nil {
		return "", e
	}
	white := func(c uint16) bool {
		return c == 9 || c == 10 || c == 11 || c == 12 || c == 13 || c == 32 || c == 160 || c == 0x1680 || (c >= 0x2000 && c <= 0x200a) || c == 0x2028 || c == 0x2029 || c == 0x202f || c == 0x205f || c == 0x3000 || c == 0xfeff
	}
	for len(u) > 0 && white(u[0]) {
		u = u[1:]
	}
	for len(u) > 0 && white(u[len(u)-1]) {
		u = u[:len(u)-1]
	}
	if len(u) > 4000 {
		u = u[:4000]
	}
	return jsjson.FromUTF16(u), nil
}

var publicHTTP = regexp.MustCompile(`^public_http_[45][0-9]{2}$`)
var failureCodes = map[string]bool{}

// CodeError is optional: code takes priority over message, exactly as the source.
type CodeError interface{ Code() string }

func FailureReason(e error) string {
	if e == nil {
		return "image_description_unavailable"
	}
	if c, ok := e.(CodeError); ok && failureCodes[c.Code()] {
		return c.Code()
	}
	if failureCodes[e.Error()] || publicHTTP.MatchString(e.Error()) {
		return e.Error()
	}
	return "image_description_unavailable"
}

var ErrCancelled = errors.New("request_cancelled")

func Describe(ctx context.Context, t *Turn, cfg Config, messages []*Message, cache *Cache, p Ports, lib *Library) (Result, error) {
	out := Result{Trust: "untrusted_image_descriptions", FramePolicy: "first_frame_only", Images: []Image{}, Omitted: []Omitted{}}
	if t == nil || p.Recalled == nil || p.SourceAvailable == nil || p.ReadImage == nil || p.ImageDataURL == nil || p.Complete == nil || p.AddDependency == nil || p.AddSourceValidation == nil {
		return out, errors.New("vision_context_composition_required")
	}
	models := func() (any, error) {
		if cfg.ReadModels != nil {
			return cfg.ReadModels()
		}
		return cfg.Models, nil
	}
	hasVision := func() (bool, error) {
		if cfg.ReadModels == nil {
			return cfg.VisionRoute, nil
		}
		config, e := cfg.ReadModels()
		if e != nil {
			return false, e
		}
		if config == nil {
			return false, errors.New("vision_model_configuration_invalid")
		}
		raw, _ := config.Get("routes")
		routes, ok := raw.([]any)
		if !ok {
			return false, errors.New("vision_model_configuration_invalid")
		}
		for _, raw := range routes {
			route, ok := raw.(*jsjson.Object)
			if !ok {
				return false, errors.New("vision_model_configuration_invalid")
			}
			role, _ := route.Get("role")
			ids, _ := route.Get("orderedModelIds")
			ordered, _ := ids.([]any)
			if role == "vision" && len(ordered) > 0 {
				return true, nil
			}
		}
		return false, nil
	}
	if cache == nil {
		cache = &Cache{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.budget == nil {
		t.budget = map[string]bool{}
		t.results = map[string]Observation{}
	}
	ranked := []Candidate{}
	order := map[*Message]int{}
	for i, m := range messages {
		if _, ok := order[m]; !ok {
			order[m] = i
		}
		if m.SceneID == cfg.SceneID && m.Status == "available" {
			for _, part := range m.Parts {
				if part.Type == "media" && (part.MediaType == "image" || imageMIME(part.MediaType)) {
					ranked = append(ranked, Candidate{m, part})
				}
			}
		}
	}
	priority := cfg.PriorityIDs
	if priority == nil {
		priority = []string{}
		for i := len(messages) - 1; i >= 0; i-- {
			priority = append(priority, messages[i].ID)
		}
	}
	rank := func(id string) int {
		for i, v := range priority {
			if v == id {
				return i
			}
		}
		return int(^uint(0) >> 1)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return rank(ranked[i].Message.ID) < rank(ranked[j].Message.ID) })
	selected := []Candidate{}
	seen := map[string]bool{}
	for _, c := range ranked {
		if cfg.Mode == "real" && ctx.Err() == nil {
			recalled, e := p.Recalled(cfg.SceneID, c.Message.ID)
			if e != nil {
				return out, e
			}
			if !recalled {
				available, e := p.SourceAvailable(c)
				if e != nil {
					return out, e
				}
				if !available {
					out.Images = append(out.Images, Image{c.Message.ID, Observation{Status: "unavailable", Reason: "image_source_unavailable"}})
					continue
				}
			}
		}
		key := pair(c)
		if seen[key] {
			continue
		}
		if !t.budget[key] && len(t.budget) >= 4 {
			out.Omitted = append(out.Omitted, Omitted{c.Message.ID, "image_budget_exhausted"})
			continue
		}
		t.budget[key] = true
		seen[key] = true
		selected = append(selected, c)
	}
	sort.SliceStable(selected, func(i, j int) bool { return order[selected[i].Message] < order[selected[j].Message] })
	if cfg.Mode != "real" || len(selected) == 0 {
		return out, nil
	}
	route := true
	if lib == nil {
		var e error
		route, e = hasVision()
		if e != nil {
			return out, e
		}
	}
	if !route {
		for _, c := range selected {
			out.Images = append(out.Images, Image{c.Message.ID, Observation{Status: "unavailable", Reason: "route_not_configured"}})
		}
		return out, nil
	}
	for _, c := range selected {
		valid := func() (bool, error) {
			if ctx.Err() != nil {
				return false, nil
			}
			recalled, e := p.Recalled(cfg.SceneID, c.Message.ID)
			if e != nil || recalled {
				return false, e
			}
			return p.SourceAvailable(c)
		}
		ensure := func() error {
			ok, e := valid()
			if e != nil {
				return e
			}
			if !ok {
				return ErrCancelled
			}
			return nil
		}
		p.AddDependency(c.Message.ID)
		p.AddSourceValidation(c)
		if e := ensure(); e != nil {
			return out, e
		}
		modelValue, e := models()
		if e != nil {
			return out, e
		}
		key, e := hash("visual-expression-v2", cfg.SceneID, c.Message.ID, c.Part.AssetID, modelValue)
		if e != nil {
			return out, e
		}
		appendObs := func(o Observation) {
			out.Images = append(out.Images, Image{c.Message.ID, o})
			if o.FramePolicy == "stored_library_observation" {
				out.FramePolicy = "per_image"
			}
		}
		if lib != nil && lib.Lookup != nil {
			stored, ok, e := lib.Lookup(c)
			if e != nil {
				return out, e
			}
			if ok && stored != "" {
				if e := ensure(); e != nil {
					return out, e
				}
				if lib.AddLookupValidation == nil {
					return out, errors.New("vision_library_validation_required")
				}
				lib.AddLookupValidation(c, stored)
				o := Observation{Status: "described", Description: stored, FramePolicy: "stored_library_observation"}
				t.results[key] = o
				appendObs(o)
				continue
			}
		}
		if previous, ok := t.results[key]; ok && (lib == nil || lib.Lookup == nil || previous.FramePolicy != "stored_library_observation") {
			if e := ensure(); e != nil {
				return out, e
			}
			appendObs(previous)
			continue
		}
		if cached, ok := cache.Get(key); ok && cached != "" {
			if e := ensure(); e != nil {
				return out, e
			}
			o := Observation{Status: "described", Description: cached}
			t.results[key] = o
			appendObs(o)
			continue
		}
		operation := "image.download"
		o, e := func() (Observation, error) {
			route, e := hasVision()
			if e != nil {
				return Observation{}, e
			}
			canRecognize := lib != nil
			if !route && lib != nil && lib.CanRecognize != nil {
				var e error
				canRecognize, e = lib.CanRecognize(c)
				if e != nil {
					return Observation{}, e
				}
			}
			if !route && !canRecognize {
				operation = "image.describe"
				return Observation{}, errors.New("route_not_configured")
			}
			bytes, e := p.ReadImage(ctx, c)
			if e != nil {
				return Observation{}, e
			}
			if e = ensure(); e != nil {
				return Observation{}, e
			}
			operation = "image.recognize"
			description := ""
			matched := false
			if lib != nil && lib.Recognize != nil {
				description, matched, e = lib.Recognize(ctx, bytes, c)
				if e != nil {
					return Observation{}, e
				}
			}
			if e = ensure(); e != nil {
				return Observation{}, e
			}
			modelValue, e := models()
			if e != nil {
				return Observation{}, e
			}
			contentKey, e := hash("visual-expression-v2", cfg.SceneID, fmt.Sprintf("%x", sha256.Sum256(bytes)), modelValue)
			if e != nil {
				return Observation{}, e
			}
			found := matched
			if content, ok := cache.Get(contentKey); !matched && ok && content != "" {
				description = content
				found = true
			}
			if !found {
				route, e := hasVision()
				if e != nil {
					return Observation{}, e
				}
				if !route {
					return Observation{}, errors.New("route_not_configured")
				}
				operation = "image.decode"
				url, e := p.ImageDataURL(ctx, bytes)
				if e != nil {
					return Observation{}, e
				}
				if e = ensure(); e != nil {
					return Observation{}, e
				}
				operation = "image.describe"
				now := time.Now()
				if cfg.Now != nil {
					now = cfg.Now()
				}
				timezone := cfg.Timezone
				if cfg.ReadTimezone != nil {
					var e error
					timezone, e = cfg.ReadTimezone()
					if e != nil {
						return Observation{}, e
					}
				}
				user, _ := jsjson.NewObject(jsjson.Member{Key: "instruction", Value: UserInstruction}, jsjson.Member{Key: "sentAt", Value: c.Message.SentAt}, jsjson.Member{Key: "now", Value: now.UTC().Format("2006-01-02T15:04:05.000Z")}, jsjson.Member{Key: "timezone", Value: timezone})
				content, e := jsjson.Stringify(user)
				if e != nil {
					return Observation{}, e
				}
				description, e = p.Complete(ctx, []ChatMessage{{Role: "system", Content: SystemPrompt}, {Role: "user", Content: content, Images: []string{url}}})
				if e != nil {
					return Observation{}, e
				}
				if e = ensure(); e != nil {
					return Observation{}, e
				}
				description, e = trimSlice(description)
				if e != nil {
					return Observation{}, e
				}
			}
			if description == "" {
				return Observation{Status: "unavailable", Reason: "empty_image_description"}, nil
			}
			policy := "first_frame_only"
			if matched {
				policy = "stored_library_observation"
			} else {
				cache.Put(key, cfg.SceneID, c.Message.ID, description)
				cache.Put(contentKey, cfg.SceneID, c.Message.ID, description)
			}
			return Observation{Status: "described", Description: description, FramePolicy: policy}, nil
		}()
		if e != nil {
			if p.ReportError != nil {
				p.ReportError(operation, e)
			}
			valid, validationError := valid()
			if validationError != nil {
				return out, validationError
			}
			if !valid {
				return out, ErrCancelled
			}
			o = Observation{Status: "unavailable", Reason: FailureReason(e)}
		}
		t.results[key] = o
		appendObs(o)
	}
	return out, nil
}

// imageMIME folds only the ASCII MIME prefix. Unicode case mappings must not
// turn a non-ASCII identifier into an image MIME type (for example U+0130).
func imageMIME(s string) bool {
	if len(s) < 6 {
		return false
	}
	want := "image/"
	for i := 0; i < 6; i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}
