package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const memePrefix = "/api/memes"

func memePath(path string) bool { return path == memePrefix || strings.HasPrefix(path, memePrefix+"/") }
func memeRoute(r *http.Request) (operation, id string, ok bool) {
	path := habitRoutingPath(r)
	method := r.Method
	if method == "HEAD" {
		method = "GET"
	}
	if path == memePrefix {
		return "list", "", method == "GET"
	}
	parts := strings.Split(strings.TrimPrefix(path, memePrefix+"/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		id = habitTryDecode(parts[0], false)
		if id == "import" && method == "POST" {
			return "import", "", true
		}
		switch method {
		case "GET":
			return "get", id, true
		case "PATCH":
			return "update", id, true
		case "DELETE":
			return "delete", id, true
		}
	}
	if len(parts) == 2 && parts[0] != "" {
		id = habitTryDecode(parts[0], false)
		switch parts[1] {
		case "file", "thumbnail":
			return parts[1], id, method == "GET"
		case "analyze", "inclusion":
			return parts[1], id, method == "POST"
		}
	}
	return "", "", false
}
func memeObject(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func memeGet(o *jsjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func memeString(v any) string { s, _ := v.(string); return s }
func memeLength(s string, max int) bool {
	n, e := jsjson.CodePointCount(s)
	return e == nil && n <= max
}
func memeQuery(raw string) (o memeservice.PageOptions, valid bool) {
	values := map[string][]string{}
	for _, pair := range strings.Split(raw, "&") {
		k, v, _ := strings.Cut(pair, "=")
		k = habitTryDecode(strings.ReplaceAll(k, "+", " "), false)
		v = habitTryDecode(strings.ReplaceAll(v, "+", " "), false)
		if k != "" {
			values[k] = append(values[k], v)
		}
	}
	// Source object strips unknown query fields, while known repeats are arrays.
	for _, key := range []string{"page", "pageSize", "query", "scope", "state"} {
		if len(values[key]) > 1 {
			return o, false
		}
	}
	number := func(key string, def, max float64) (*float64, bool) {
		n := def
		if v, has := values[key]; has {
			text := strings.TrimFunc(v[0], func(r rune) bool {
				return strings.ContainsRune(" \t\n\r\v\f\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff", r)
			})
			var e error
			if text == "" {
				n = 0
			} else if len(text) > 2 && text[0] == '0' && strings.ContainsAny(text[1:2], "xXoObB") {
				var u uint64
				u, e = strconv.ParseUint(text, 0, 64)
				n = float64(u)
			} else {
				if !experienceDecimal.MatchString(text) {
					return nil, false
				}
				n, e = strconv.ParseFloat(text, 64)
			}
			if e != nil {
				return nil, false
			}
		}
		return &n, !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n && n >= 1 && n <= max
	}
	var ok bool
	o.Page, ok = number("page", 1, 1000000)
	if !ok {
		return o, false
	}
	o.PageSize, ok = number("pageSize", 24, 96)
	if !ok {
		return o, false
	}
	o.Scope = "*"
	o.State = "*"
	if v := values["query"]; len(v) > 0 {
		o.Query = v[0]
	}
	if v := values["scope"]; len(v) > 0 {
		o.Scope = v[0]
	}
	if v := values["state"]; len(v) > 0 {
		o.State = v[0]
	}
	return o, memeLength(o.Query, 200) && memeLength(o.Scope, 300) && (experienceMember(o.Scope, "*", "shared", "local") || strings.HasPrefix(o.Scope, "scene:")) && experienceMember(o.State, "*", "favorite", "disabled", "available", "unanalyzed", "known")
}
func (s *Server) memeBody(operation string, raw []byte) (*jsjson.Object, bool) {
	value, e := jsjson.ParseBytes(raw)
	if e != nil {
		return nil, false
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return nil, false
	}
	keys := map[string]bool{}
	switch operation {
	case "inclusion":
		keys["included"] = true
		if _, ok := memeGet(o, "included").(bool); !ok {
			return nil, false
		}
	case "analyze":
		keys["sceneId"] = true
		if _, ok := memeGet(o, "sceneId").(string); !ok {
			return nil, false
		}
	case "import":
		for _, k := range []string{"sceneId", "messageId", "base64"} {
			keys[k] = true
			if _, ok := memeGet(o, k).(string); !ok {
				return nil, false
			}
		}
		if !memeLength(memeString(memeGet(o, "base64")), 89478488) {
			return nil, false
		}
	case "update":
		for _, k := range []string{"available", "shared", "privacyReviewed", "favorite", "disabled"} {
			keys[k] = true
			if v, has := o.Get(k); has {
				if _, ok := v.(bool); !ok {
					return nil, false
				}
			}
		}
		keys["tags"] = true
		if tags, has := o.Get("tags"); has {
			raw, ok := tags.(*jsjson.Object)
			if !ok {
				return nil, false
			}
			clean := new(jsjson.Object)
			for _, key := range memeservice.Categories {
				items, ok := memeGet(raw, key).([]any)
				if !ok {
					return nil, false
				}
				for _, v := range items {
					if _, ok := v.(string); !ok {
						return nil, false
					}
				}
				_ = clean.Set(key, items)
			}
			text, ok := memeGet(raw, "ocr").(string)
			if !ok {
				return nil, false
			}
			_ = clean.Set("ocr", text)
			_ = o.Set("tags", clean)
		}
	default:
		return nil, false
	}
	for _, k := range o.Keys() {
		if !keys[k] {
			return nil, false
		}
	}
	return o, true
}

// Node Buffer.from(base64) accepts URL alphabet, ignores non-alphabet bytes and
// stops at padding. Admission, not this decoder, enforces original byte policy.
func memeBase64(s string) []byte {
	clean := strings.Builder{}
	for _, r := range s {
		if r == '=' {
			break
		}
		if r == '-' {
			r = '+'
		}
		if r == '_' {
			r = '/'
		}
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' {
			clean.WriteRune(r)
		}
	}
	v := clean.String()
	if len(v)%4 == 1 {
		v = v[:len(v)-1]
	}
	b, _ := base64.RawStdEncoding.DecodeString(v)
	return b
}
func (s *Server) serveMemes(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	report := func(e error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "meme.failed", Message: e.Error(), RequestID: requestID})
		}
	}
	fail := func(e error) {
		report(e)
		if r.Method == "HEAD" {
			w.WriteHeader(503)
		} else {
			habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
		}
	}
	if !available {
		fail(errors.New("meme_request_unavailable"))
		return
	}
	ctx, release, e := s.memes.RootContext(r.Context())
	if e != nil {
		fail(e)
		return
	}
	defer release()
	publish := func(status int, b []byte) error {
		response.status = status
		return s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return b, nil }, func(b []byte) error { return response.enqueue(ctx, b) })
	}
	operation, id, route := memeRoute(r)
	if !route {
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		e = publish(404, []byte("404 Not Found"))
	} else {
		e = s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		if e != nil {
			fail(e)
			return
		}
		var input *jsjson.Object
		if operation == "update" || operation == "import" || operation == "analyze" || operation == "inclusion" {
			if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
				fail(errors.New("meme_json_content_type_required"))
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				fail(err)
				return
			}
			if _, err = jsjson.ParseBytes(raw); err != nil {
				fail(err)
				return
			}
			var valid bool
			input, valid = s.memeBody(operation, raw)
			if !valid {
				e = publish(400, []byte(`{"error":"Invalid request"}`))
				goto drain
			}
		}
		e = s.runMemeRoute(ctx, operation, id, input, r, publish, response)
	}
drain:
	if response.accepted {
		e = errors.Join(e, response.drain(ctx, w))
	}
	if e != nil {
		if !response.accepted && r.Context().Err() == nil {
			fail(e)
		} else {
			report(e)
		}
	}
}
func (s *Server) runMemeRoute(ctx context.Context, operation, id string, input *jsjson.Object, r *http.Request, publish func(int, []byte) error, response *publicationResponse) error {
	owner := s.memes.Memes
	var asset *jsjson.Object
	var body []byte
	var e error
	switch operation {
	case "list":
		o, valid := memeQuery(r.URL.RawQuery)
		if !valid {
			return publish(400, []byte(`{"error":"Invalid request"}`))
		}
		return owner.PublishPage(ctx, o, func(b []byte) error { response.status = 200; return response.enqueue(ctx, b) })
	case "get":
		asset, e = owner.AdminGet(ctx, id)
	case "file":
		asset, body, e = owner.AdminFile(ctx, id)
		if asset != nil {
			response.header.Set("Content-Type", "image/"+memeString(memeGet(asset, "format")))
			response.header.Set("Cache-Control", "no-store")
		}
	case "thumbnail":
		body, e = owner.Thumbnail(ctx, id, s.memes.Codec)
		if e == nil {
			asset, e = owner.AdminGet(ctx, id)
		}
		response.header.Set("Content-Type", "image/webp")
		response.header.Set("Cache-Control", "no-store")
	case "update":
		asset, e = owner.Update(ctx, id, input)
	case "inclusion":
		asset, e = owner.AdminSetIncluded(ctx, id, memeGet(input, "included") == true)
	case "delete":
		var ok bool
		ok, e = owner.AdminDelete(ctx, id)
		if e != nil {
			return e
		}
		body, e = jsjson.Marshal(memeObject("ok", ok))
		if e != nil {
			return e
		}
		response.status = 200
		return owner.PublishAsset(ctx, id, nil, body, func(b []byte) error { return response.enqueue(ctx, b) })
	case "import":
		var projection *memeservice.AssetProjection
		asset, projection, e = owner.CaptureProjection(ctx, memeString(memeGet(input, "sceneId")), func(operation context.Context) (*jsjson.Object, error) {
			return s.memes.Import(operation, memeString(memeGet(input, "sceneId")), memeString(memeGet(input, "messageId")), memeBase64(memeString(memeGet(input, "base64"))))
		})
		if e == nil && asset != nil {
			body, e = jsjson.Marshal(asset)
			if e != nil {
				return e
			}
			response.status = 200
			return owner.PublishProjection(ctx, projection, body, func(b []byte) error { return response.enqueue(ctx, b) })
		}
		if e != nil && e.Error() == "meme_import_real_source_required" {
			return publish(400, []byte(`{"error":"An available real source message is required"}`))
		}
	case "analyze":
		var projection *memeservice.AssetProjection
		asset, projection, e = owner.CaptureProjection(ctx, memeString(memeGet(input, "sceneId")), func(operation context.Context) (*jsjson.Object, error) {
			return s.memes.Analyze(operation, memeString(memeGet(input, "sceneId")), id)
		})
		if e == nil && asset != nil {
			body, e = jsjson.Marshal(asset)
			if e != nil {
				return e
			}
			response.status = 200
			return owner.PublishProjection(ctx, projection, body, func(b []byte) error { return response.enqueue(ctx, b) })
		}
	}
	if e != nil {
		return e
	}
	if asset == nil {
		return publish(400, []byte(`{"error":"Asset unavailable"}`))
	}
	if body == nil {
		body, e = jsjson.Marshal(asset)
		if e != nil {
			return e
		}
	}
	id = memeString(memeGet(asset, "id"))
	response.status = 200
	return owner.PublishAsset(ctx, id, asset, body, func(b []byte) error { return response.enqueue(ctx, b) })
}
