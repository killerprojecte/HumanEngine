// Package memeengine composes native MemeService with real source/runtime and
// model-read owners. No permissive model or source authorization is supplied.
package memeengine

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strings"
)

type Object = jsjson.Object
type Service struct {
	Sources   *sourceevents.Store
	TurnRead  *modelread.RuntimePolicy
	main      *sql.DB
	authority *recovery.Authority
	Memes     *memeservice.Service
	Runtime   *botruntime.Runtime
	Habits    *habitservice.Service
	Codec     *imagecodec.Compressor
	Models    *providertransport.NativeModels
	Read      *modelread.Policy
}

func New(main *sql.DB, a *recovery.Authority, memes *memeservice.Service, runtime *botruntime.Runtime, habits *habitservice.Service, store *assertions.Store, sources *sourceevents.Store, codec *imagecodec.Compressor, models *providertransport.NativeModels) (*Service, error) {
	if main == nil || a == nil || !memes.OwnedBy(main, a) || runtime == nil || habits == nil || !habits.OwnedBy(main, a) || store == nil || codec == nil || models == nil {
		return nil, errors.New("meme_engine_dependencies_required")
	}
	if e := store.CheckReadBinding(a, main, nil); e != nil {
		return nil, e
	}
	read, e := modelread.New(store)
	if e != nil {
		return nil, e
	}
	turnRead, e := modelread.NewRuntime(store, sources, main, a)
	if e != nil {
		return nil, e
	}
	return &Service{main: main, authority: a, Memes: memes, Runtime: runtime, Habits: habits, Codec: codec, Models: models, Read: read, Sources: sources, TurnRead: turnRead}, nil
}
func (s *Service) OwnedBy(main *sql.DB, a *recovery.Authority) bool {
	return s != nil && s.main == main && s.authority == a
}

// RootContext is exclusively for verified external HTTP/Engine roots. Nested
// calls pass their inherited context; no service fallback replaces ownership.
func (s *Service) RootContext(ctx context.Context) (context.Context, func(), error) {
	return s.authority.OperationContext(ctx, s.main)
}
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(v any) string  { s, _ := v.(string); return s }
func arr(v any) []any   { a, _ := v.([]any); return a }
func obj(v any) *Object { o, _ := v.(*Object); return o }
func fields(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func prefix(s string, n int) string {
	u, e := jsjson.UTF16(s)
	if e != nil {
		return ""
	}
	if len(u) > n {
		u = u[:n]
	}
	return jsjson.FromUTF16(u)
}
func (s *Service) Import(ctx context.Context, scene, id string, b []byte) (*Object, error) {
	snapshot, e := s.Runtime.Snapshot(scene)
	if e != nil {
		if e.Error() == "scene_missing" {
			return nil, errors.New("meme_import_real_source_required")
		}
		return nil, e
	}
	var message *Object
	for _, row := range snapshot.Messages {
		if get(row, "id") == id {
			message = row
			break
		}
	}
	if message == nil || get(message, "origin") == "simulation" || get(message, "senderId") == "bot" {
		return nil, errors.New("meme_import_real_source_required")
	}
	source := fields("sceneId", scene, "messageId", id, "senderId", get(message, "senderId"))
	evidence := fields("sceneId", scene, "messageId", id, "senderId", get(message, "senderId"), "available", get(message, "status") == "available", "isBot", false)
	return s.Memes.Intake(ctx, memeservice.Context{SceneID: scene, SourceEvidence: []*Object{evidence}}, b, source, false, s.Codec)
}
func (s *Service) Analyze(ctx context.Context, scene, id string) (*Object, error) {
	ticket, e := s.Read.Require(ctx, scene)
	if e != nil {
		return nil, e
	}
	if !ticket.PersonalizationEnabled() {
		return nil, errors.New("personalization_disabled")
	}
	snapshot, e := s.Runtime.Snapshot(scene)
	if e != nil {
		return nil, e
	}
	asset, e := s.Memes.AdminGet(ctx, id)
	if e != nil {
		return nil, e
	}
	sourceIndex := -1
	known := false
	for i, message := range snapshot.Messages {
		for _, v := range arr(get(asset, "sources")) {
			source := obj(v)
			if get(source, "sceneId") == scene && get(source, "messageId") == get(message, "id") {
				sourceIndex = i
				if get(message, "status") == "available" {
					known = true
				}
			}
		}
	}
	rows := []*Object{}
	if sourceIndex >= 0 {
		start := sourceIndex - 2
		if start < 0 {
			start = 0
		}
		end := sourceIndex + 3
		if end > len(snapshot.Messages) {
			end = len(snapshot.Messages)
		}
		rows = append(rows, snapshot.Messages[start:end]...)
	}
	start := len(snapshot.Messages) - 8
	if start < 0 {
		start = 0
	}
	rows = append(rows, snapshot.Messages[start:]...)
	indexes := map[string]int{}
	unique := []*Object{}
	for _, row := range rows {
		id := str(get(row, "id"))
		if index, ok := indexes[id]; ok {
			unique[index] = row
		} else {
			indexes[id] = len(unique)
			unique = append(unique, row)
		}
	}
	messages := []any{}
	for _, row := range unique {
		texts := []string{}
		for _, part := range arr(get(row, "parts")) {
			p := obj(part)
			if get(p, "type") == "text" {
				texts = append(texts, str(get(p, "text")))
			}
		}
		messages = append(messages, fields("isBot", get(row, "senderId") == "bot", "text", prefix(strings.Join(texts, " "), 2000)))
	}
	hints, e := s.Habits.List(ctx, habitservice.Context{SceneID: scene})
	if e != nil {
		return nil, e
	}
	expression := []any{}
	for _, hint := range hints {
		if get(hint, "kind") == "botExpression" {
			expression = append(expression, hint)
		}
	}
	matches, e := s.Memes.Search(ctx, memeservice.Context{SceneID: scene}, "", nil)
	if e != nil {
		return nil, e
	}
	favorites := []any{}
	for _, a := range matches {
		if get(a, "favorite") == true {
			favorites = append(favorites, get(a, "tags"))
			if len(favorites) == 5 {
				break
			}
		}
	}
	persona, e := s.persona(ctx)
	if e != nil {
		return nil, e
	}
	guarded := providertransport.WithModelReadGuard(ctx, func(c context.Context) error { return s.Read.Assert(c, ticket) })
	return s.Memes.Analyze(guarded, memeservice.Context{SceneID: scene}, id, s.Models, s.Codec, memeservice.AnalyzeOptions{Force: true, Selection: &memeservice.SelectionContext{Persona: persona, Preferences: fields("expression", expression, "favorites", favorites), SourceContextKnown: known, SourceContext: messages}})
}

func (s *Service) persona(ctx context.Context) (string, error) {
	var persona string
	e := s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		var raw string
		e := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&raw)
		if e == sql.ErrNoRows {
			raw = string(contract.DefaultSettingsInput())
		} else if e != nil {
			return nil, e
		}
		schemas, e := contract.NewSchemas()
		if e != nil {
			return nil, e
		}
		v, e := schemas.Parse("Settings", []byte(raw))
		if e != nil {
			return nil, e
		}
		persona, _ = v["persona"].(string)
		return nil, nil
	}, func([]byte) error { return nil })
	if e != nil {
		return "", e
	}
	return persona, nil
}
