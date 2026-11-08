package learningowner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

type indexState struct {
	active, job  *Object
	incompatible string
	selected     *selectedEncoder
	empty        bool
}
type selectedEncoder struct {
	model    providertransport.EmbeddingModel
	provider providertransport.Provider
	revision string
}
type indexRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type indexQuery func(string, ...any) (indexRows, error)

func indexObject(v any) *Object { o, _ := v.(*Object); return o }
func indexHash(v any) (string, error) {
	raw, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:]), nil
}
func selectEncoder(config *Object) (*selectedEncoder, error) {
	id := ""
	for _, v := range arr(get(config, "routes")) {
		r := indexObject(v)
		if str(r, "role") == "embedding" {
			ids := arr(get(r, "orderedModelIds"))
			if len(ids) > 0 {
				id, _ = ids[0].(string)
			}
			break
		}
	}
	var model, provider *Object
	for _, v := range arr(get(config, "models")) {
		m := indexObject(v)
		if str(m, "id") == id {
			model = m
			break
		}
	}
	if model == nil {
		return nil, nil
	}
	capable := false
	for _, c := range arr(get(model, "capabilities")) {
		if c == "embedding" {
			capable = true
		}
	}
	if !capable {
		return nil, nil
	}
	for _, v := range arr(get(config, "providers")) {
		p := indexObject(v)
		if str(p, "id") == str(model, "providerId") {
			provider = p
			break
		}
	}
	if provider == nil {
		return nil, nil
	}
	endpoint := str(provider, "baseUrl")
	text, e := jsjson.UTF8Text(endpoint)
	if e != nil {
		return nil, e
	}
	u, e := whatwg.Parse(text)
	if e != nil {
		return nil, errors.New("invalid_embedding_endpoint")
	}
	if u.Username() != "" || u.Password() != "" || u.Search() != "" || u.Hash() != "" {
		return nil, errors.New("embedding_endpoint_must_not_contain_credentials_or_query")
	}
	origin := "null"
	switch u.Protocol() {
	case "http:", "https:", "ws:", "wss:", "ftp:":
		origin = u.Protocol() + "//" + u.Host()
	}
	revision, e := indexHash([]any{get(provider, "id"), get(provider, "protocol"), origin + u.Pathname(), get(model, "id"), get(model, "apiModelName"), get(model, "embeddingDimensions"), "text-v1", false})
	if e != nil {
		return nil, e
	}
	selected := &selectedEncoder{model: providertransport.EmbeddingModel{ID: id, ProviderID: str(model, "providerId"), APIModelName: str(model, "apiModelName")}, provider: providertransport.Provider{ID: str(provider, "id"), Protocol: str(provider, "protocol"), BaseURL: endpoint}, revision: revision}
	if n, ok := get(model, "embeddingDimensions").(float64); ok {
		i := int(n)
		selected.model.Dimensions = &i
	}
	if n, ok := get(model, "embeddingBatchSize").(float64); ok {
		i := int(n)
		selected.model.BatchSize = &i
	}
	return selected, nil
}
func readIndexState(query indexQuery, checkEmpty bool) (out indexState, err error) {
	rows, e := query("SELECT key,value FROM config WHERE key IN ('models','embedding.active','embedding.rebuild','embedding.incompatible')")
	if e != nil {
		return out, e
	}
	values := map[string]any{}
	for rows.Next() {
		var key, raw string
		if e = rows.Scan(&key, &raw); e != nil {
			rows.Close()
			return out, e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			rows.Close()
			return out, e
		}
		values[key] = v
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return out, e
	}
	out.active = indexObject(values["embedding.active"])
	out.job = indexObject(values["embedding.rebuild"])
	out.incompatible, _ = values["embedding.incompatible"].(string)
	out.selected, e = selectEncoder(indexObject(values["models"]))
	if e != nil && !checkEmpty {
		return out, e
	}
	if checkEmpty {
		rows, e = query("SELECT EXISTS(SELECT id FROM memories) OR EXISTS(SELECT id FROM vector_spaces)")
		if e != nil {
			return out, e
		}
		var nonempty bool
		if rows.Next() {
			e = rows.Scan(&nonempty)
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return out, e
		}
		out.empty = !nonempty && out.active == nil && out.job == nil
	}
	return out, nil
}
func (o *Owner) indexState(ctx context.Context) (out indexState, err error) {
	err = o.d.Authority.Publish(ctx, o.d.Main, func(conn *sql.Conn) ([]byte, error) {
		var e error
		out, e = readIndexState(func(q string, args ...any) (indexRows, error) { return conn.QueryContext(ctx, q, args...) }, false)
		return nil, e
	}, func([]byte) error { return nil })
	return
}
func saveIndexValue(ws *recovery.WriteSession, key string, value any) error {
	raw, e := jsjson.Stringify(value)
	if e != nil {
		return e
	}
	_, e = ws.Exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, raw)
	return e
}
func (o *Owner) initializeIndex(ctx context.Context) error {
	return o.d.Authority.WithWriteSession(ctx, o.d.Main, func(ws *recovery.WriteSession) error {
		state, e := readIndexState(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) }, true)
		if e != nil {
			return e
		}
		o.indexBootstrap = state.empty
		if str(state.job, "state") == "running" {
			lease, _ := get(state.job, "leaseUntil").(float64)
			if lease <= float64(o.d.Now().UnixMilli()) {
				_ = state.job.Set("state", "paused")
				reason := "lease_expired"
				if get(state.job, "requiresRetryConfirmation") == true {
					reason = "lease_expired_uncertain_request"
				}
				_ = state.job.Set("error", reason)
				return saveIndexValue(ws, "embedding.rebuild", state.job)
			}
		}
		return nil
	})
}
func indexUsable(s indexState, a *Object) bool {
	return a != nil && s.active != nil && str(s.active, "generationId") == str(a, "generationId") && s.incompatible != str(a, "generationId") && s.selected != nil && s.selected.revision == str(a, "encoderRevision")
}
func (o *Owner) markIndexIncompatible(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return o.d.Authority.WithWriteSession(ctx, o.d.Main, func(ws *recovery.WriteSession) error {
		s, e := readIndexState(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) }, false)
		if e != nil {
			return e
		}
		if str(s.active, "generationId") != id {
			return nil
		}
		return saveIndexValue(ws, "embedding.incompatible", id)
	})
}
func (o *Owner) embeddingGuard(ctx context.Context, record *Object) (context.Context, func(*recovery.WriteSession) error, error) {
	scope := indexObject(get(record, "scope"))
	if str(scope, "kind") != "scene" {
		return ctx, func(*recovery.WriteSession) error { return nil }, nil
	}
	ticket, e := o.d.Read.Require(context.WithoutCancel(ctx), str(scope, "sceneId"))
	if e != nil {
		return ctx, nil, e
	}
	if !ticket.PersonalizationEnabled() && (str(scope, "personId") != "" || str(record, "kind") == "experience" || str(record, "kind") == "summary") {
		return ctx, nil, errors.New("personalization_disabled")
	}
	return o.readGuard(ctx, ticket), func(ws *recovery.WriteSession) error {
		if ws != nil {
			if !o.d.Read.ValidInSession(context.WithoutCancel(ctx), ticket, ws) {
				return errors.New("memory_read_revoked")
			}
			return nil
		}
		return o.d.Read.Assert(context.WithoutCancel(ctx), ticket)
	}, nil
}
func (o *Owner) readGuard(ctx context.Context, ticket *modelread.Ticket) context.Context {
	return providertransport.WithModelReadGuard(ctx, func(c context.Context) error { return o.d.Read.Assert(context.WithoutCancel(c), ticket) })
}

// Source reads the active pointer before its best-effort catch block. A
// failure there remains a post-commit error; provider/index failures are logged.
func (o *Owner) indexMemory(ctx context.Context, id, content string) error {
	if ctx.Err() != nil {
		return nil
	}
	var active *Object
	e := o.d.Authority.Publish(ctx, o.d.Main, func(conn *sql.Conn) ([]byte, error) {
		var raw string
		e := conn.QueryRowContext(ctx, "SELECT value FROM config WHERE key='embedding.active'").Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		v, e := jsjson.Parse(raw)
		active = indexObject(v)
		return nil, e
	}, func([]byte) error { return nil })
	if e != nil {
		return e
	}
	if e = o.indexMemoryRun(ctx, id, content, active); e != nil {
		o.log("warn", "embedding_unavailable")
	}
	return nil
}
func (o *Owner) indexMemoryRun(ctx context.Context, id, content string, active *Object) error {
	state, e := o.indexState(ctx)
	if e != nil {
		return e
	}
	selected := state.selected
	if selected == nil || (active == nil && !o.indexBootstrap) || (active != nil && !indexUsable(state, active)) {
		return nil
	}
	before, e := o.d.Memory.IndexableByIDs(ctx, []string{id})
	if e != nil {
		return e
	}
	if len(before) == 0 || str(before[0], "content") != content {
		return nil
	}
	guarded, admit, e := o.embeddingGuard(ctx, before[0])
	if e != nil {
		return e
	}
	result, e := o.d.Models.Embeddings(guarded, selected.model, []string{content})
	if e != nil {
		if e.Error() == "embedding_dimensions_mismatch" {
			current, x := o.indexState(context.WithoutCancel(ctx))
			if x == nil {
				generation := str(active, "generationId")
				if generation == "" && str(current.active, "encoderRevision") == selected.revision {
					generation = str(current.active, "generationId")
				}
				_ = o.markIndexIncompatible(context.WithoutCancel(ctx), generation)
			}
		}
		return e
	}
	if ctx.Err() != nil {
		return nil
	}
	if len(result.Vectors) == 0 {
		return nil
	}
	// Revalidate policy after physical model completion, then persist only inside
	// the real writer capability. No connection is held during the network call.
	if e = admit(nil); e != nil {
		return e
	}
	return o.d.Authority.WithWriteSession(ctx, o.d.Main, func(ws *recovery.WriteSession) error {
		if e := admit(ws); e != nil {
			return e
		}
		now, e := readIndexState(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) }, false)
		if e != nil {
			return e
		}
		if now.selected == nil || now.selected.revision != selected.revision {
			return nil
		}
		records, e := o.d.Memory.IndexableByIDsInSession(ctx, ws, []string{id})
		if e != nil {
			return e
		}
		if len(records) == 0 || str(records[0], "content") != content {
			return nil
		}
		if active == nil {
			if !o.indexBootstrap || now.job != nil {
				return nil
			}
			if now.active != nil {
				active = now.active
			} else {
				dimensions := len(result.Vectors[0])
				if dimensions == 0 {
					return nil
				}
				if dimensions > 16384 || selected.provider.ID == "" || selected.model.ID == "" || selected.model.APIModelName == "" || selected.provider.BaseURL == "" {
					return errors.New("invalid_vector_space")
				}
				generation, e := indexID()
				if e != nil {
					return e
				}
				space := obj("providerId", selected.provider.ID, "modelId", selected.model.ID, "apiModelName", selected.model.APIModelName, "deployment", selected.provider.BaseURL, "dimensions", float64(dimensions), "preprocessing", "text-v1", "normalized", false, "generation", generation, "encoderRevision", selected.revision)
				spaceID, e := indexHash(space)
				if e != nil {
					return e
				}
				raw, e := jsjson.Stringify(space)
				if e != nil {
					return e
				}
				if _, e = ws.Exec("INSERT OR IGNORE INTO vector_spaces VALUES(?,?)", spaceID, raw); e != nil {
					return e
				}
				active = obj("generationId", generation, "encoderRevision", selected.revision, "modelId", selected.model.ID, "dimensions", float64(dimensions), "spaceId", spaceID)
				if e = saveIndexValue(ws, "embedding.active", active); e != nil {
					return e
				}
				now.active = active
			}
		} else if str(now.active, "generationId") != str(active, "generationId") {
			return nil
		}
		if !indexUsable(now, active) {
			return nil
		}
		if get(active, "dimensions") != float64(len(result.Vectors[0])) {
			return saveIndexValue(ws, "embedding.incompatible", str(active, "generationId"))
		}
		return o.d.Memory.IndexInSession(ctx, ws, id, str(active, "spaceId"), result.Vectors[0])
	})
}
func indexID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
