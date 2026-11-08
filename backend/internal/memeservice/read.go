package memeservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"golang.org/x/text/unicode/norm"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

type PageOptions struct {
	Page, PageSize      *float64
	Query, Scope, State string
}
type Page struct {
	Items          []*Object
	Total          int
	Page, PageSize float64
}

func (s *Service) AdminGet(ctx context.Context, id string) (out *Object, e error) {
	e = s.transaction(ctx, func(p *scope) error { var e error; out, e = p.asset(id); return e })
	return
}
func (s *Service) AdminPage(ctx context.Context, o PageOptions) (out Page, e error) {
	size := 24.0
	if o.PageSize != nil {
		size = *o.PageSize
	}
	size = math.Max(1, math.Min(96, math.Floor(size)))
	out.PageSize = size
	e = s.transaction(ctx, func(p *scope) error {
		clauses := []string{}
		args := []any{}
		if o.Scope == "shared" || o.Scope == "local" {
			clauses = append(clauses, "json_extract(value,'$.shared') = ?")
			value := 0
			if o.Scope == "shared" {
				value = 1
			}
			args = append(args, value)
		} else if strings.HasPrefix(o.Scope, "scene:") {
			clauses = append(clauses, "EXISTS (SELECT 1 FROM meme_sources s WHERE s.asset_id=a.id AND s.scene_id=?)")
			args = append(args, strings.TrimPrefix(o.Scope, "scene:"))
		}
		states := map[string]string{"favorite": "json_extract(value,'$.favorite')=1", "disabled": "json_extract(value,'$.disabled')=1", "unanalyzed": "json_extract(value,'$.analyzed')=0", "known": "json_extract(value,'$.knownOnly')=1 AND json_extract(value,'$.disabled')=0", "available": "json_extract(value,'$.available')=1 AND COALESCE(json_extract(value,'$.knownOnly'),0)=0 AND json_extract(value,'$.disabled')=0 AND json_extract(value,'$.analyzed')=1 AND EXISTS (SELECT 1 FROM meme_sources s WHERE s.asset_id=a.id)"}
		if state := states[o.State]; state != "" {
			clauses = append(clauses, state)
		}
		query := strings.TrimFunc(o.Query, jsSpace)
		if query != "" {
			known, e := concepts(query)
			if e != nil {
				return e
			}
			if len(known) > 0 {
				marks := []string{}
				for _, k := range known {
					marks = append(marks, "?")
					args = append(args, k)
				}
				clauses = append(clauses, "EXISTS (SELECT 1 FROM meme_search_concepts c WHERE c.asset_id=a.id AND c.concept IN ("+strings.Join(marks, ",")+"))")
			} else {
				clauses = append(clauses, `EXISTS (SELECT 1 FROM json_tree(a.value,'$.tags') tag WHERE tag.type='text' AND tag.value LIKE ? ESCAPE '\')`)
				args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)+"%")
			}
		}
		where := ""
		if len(clauses) > 0 {
			where = " WHERE (" + strings.Join(clauses, ") AND (") + ")"
		}
		rows, e := p.ws.Query("SELECT COUNT(*) FROM meme_assets a"+where, args...)
		if e != nil {
			return e
		}
		if rows.Next() {
			e = rows.Scan(&out.Total)
		}
		rows.Close()
		if e != nil {
			return e
		}
		page := 1.0
		if o.Page != nil {
			page = *o.Page
		}
		out.Page = math.Max(1, math.Min(math.Max(1, math.Ceil(float64(out.Total)/size)), math.Floor(page)))
		out.Items, e = p.queryAssets("SELECT value FROM meme_assets a"+where+" ORDER BY rowid DESC LIMIT ? OFFSET ?", append(args, size, (out.Page-1)*size)...)
		return e
	})
	return
}
func jsSpace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == '\u2028' || r == '\u2029' || r == '\ufeff' || unicode.Is(unicode.Zs, r)
}
func normalized(v string) (string, error) {
	v, e := jsstring.Lower(norm.NFKC.String(v))
	return strings.TrimFunc(v, jsSpace), e
}
func tagConcepts(tags *Object) ([]string, error) {
	texts := []string{}
	for _, k := range Categories {
		for _, v := range arr(get(tags, k)) {
			texts = append(texts, str(v))
		}
	}
	texts = append(texts, str(get(tags, "ocr")))
	seen := map[string]bool{}
	out := []string{}
	for _, text := range texts {
		cs, e := concepts(text)
		if e != nil {
			return nil, e
		}
		for _, c := range cs {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}
func (s *Service) Search(ctx context.Context, c Context, query string, tags []string) (out []*Object, e error) {
	q, e := normalized(query)
	if e != nil {
		return nil, e
	}
	terms := strings.FieldsFunc(q, jsSpace)
	requested := map[string]bool{}
	for _, text := range append([]string{query}, tags...) {
		cs, e := concepts(text)
		if e != nil {
			return nil, e
		}
		for _, c := range cs {
			requested[c] = true
		}
	}
	e = s.transaction(ctx, func(p *scope) error {
		all, e := p.candidates(c, "", "")
		if e != nil {
			return e
		}
		type scored struct {
			a     *Object
			score float64
		}
		matches := []scored{}
		for _, a := range all {
			ok, e := p.visible(c, a, true)
			if e != nil {
				return e
			}
			_, deleting := s.epoch(str(get(a, "id")))
			if !ok || deleting || !yes(get(a, "analyzed")) {
				continue
			}
			labels := []string{}
			aTags := obj(get(a, "tags"))
			for _, k := range Categories {
				for _, v := range arr(get(aTags, k)) {
					lower, e := jsstring.Lower(str(v))
					if e != nil {
						return e
					}
					labels = append(labels, lower)
				}
			}
			text, e := jsstring.Lower(strings.Join(labels, " ") + " " + str(get(aTags, "ocr")))
			if e != nil {
				return e
			}
			match := 0
			for _, term := range terms {
				known, e := concepts(term)
				if e != nil {
					return e
				}
				if len(known) == 0 && strings.Contains(text, term) {
					match++
				}
			}
			for _, tag := range tags {
				tag, e := normalized(tag)
				if e != nil {
					return e
				}
				for _, label := range labels {
					if label == tag {
						match++
						break
					}
				}
			}
			cs, e := tagConcepts(aTags)
			if e != nil {
				return e
			}
			for _, key := range cs {
				if requested[key] {
					match++
				}
			}
			if (len(terms) > 0 || len(tags) > 0) && match == 0 {
				continue
			}
			score := float64(match) + num(get(a, "preference"))*0.1
			if yes(get(a, "favorite")) {
				score += 0.2
			}
			if last := str(get(a, "lastUsedAt")); last != "" {
				millis, valid, e := s.policy.DateParser.ParseMillis(last)
				if e != nil {
					return e
				}
				if valid && time.Now().UnixMilli()-millis < 600000 {
					score -= 0.3
				}
			}
			matches = append(matches, scored{a, score})
		}
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
		out = []*Object{}
		for _, m := range matches[:min(len(matches), 12)] {
			out = append(out, sanitize(m.a))
		}
		return nil
	})
	return
}

type Original struct {
	Path, ContentType string
	Bytes             float64
	Hash              string
}

func (s *Service) original(ctx context.Context, c Context, id string, available bool) (out Original, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		a, e := p.asset(id)
		if e != nil {
			return e
		}
		ok, e := p.visible(c, a, available)
		if e != nil {
			return e
		}
		_, deleting := s.epoch(id)
		if !ok || deleting {
			return errors.New("meme_not_available")
		}
		out.Path, e = s.file(id, str(get(a, "format")))
		out.ContentType = "image/" + str(get(a, "format"))
		out.Bytes = num(get(a, "bytes"))
		out.Hash = content(a)
		return e
	})
	return
}
func (s *Service) Original(ctx context.Context, c Context, id string) (Original, error) {
	return s.original(ctx, c, id, true)
}
func (s *Service) RecognitionOriginal(ctx context.Context, c Context, id string) (Original, error) {
	return s.original(ctx, c, id, false)
}
func (s *Service) HasRecognitionCandidates(ctx context.Context, c Context) (found bool, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		all, e := p.candidates(c, "", "")
		if e != nil {
			return e
		}
		for _, a := range all {
			ok, e := p.visible(c, a, false)
			if e != nil {
				return e
			}
			if ok {
				found = true
				return nil
			}
		}
		return nil
	})
	return
}

// Exact JSON is retained for qualification/differential adapters.
func stringify(v any) string { s, _ := jsjson.Stringify(v); return s }
