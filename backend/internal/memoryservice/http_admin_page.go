package memoryservice

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

type AdminPageQuery struct {
	Page                                   int64
	Limit                                  int
	Scene, Person, Kind, Status, Q, Locale string
}

// PublishPage keeps the legacy eager record presentation and its errors. Only
// response selection is paged. A nil query denotes invalid source query input.
func (a *AdminRead) PublishPage(ctx context.Context, q *AdminPageQuery, emit func(bool, []byte) error) (bool, error) {
	var locale *topiccollation.RequestedLocale
	var localeErr error
	if q != nil {
		locale, localeErr = topiccollation.NewRequested(q.Locale)
		if locale != nil {
			defer locale.Close()
		}
	}
	invalid := q == nil || errors.Is(localeErr, topiccollation.ErrLocale)
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		if invalid {
			return []byte(`{"error":"Invalid request"}`), nil
		}
		if localeErr != nil {
			return nil, localeErr
		}
		records, err := adminRecords(r)
		if err != nil {
			return nil, err
		}
		return adminPage(records, *q, locale)
	}, func(body []byte) error { return emit(invalid, body) })
}

func adminPage(records []any, q AdminPageQuery, locale *topiccollation.RequestedLocale) ([]byte, error) {
	scene, scoped := strings.CutPrefix(q.Scene, "scene:")
	if !scoped {
		scene = ""
	}
	person := jsstring.Trim(q.Person)
	search, err := locale.Lower(jsstring.Trim(q.Q))
	if err != nil {
		return nil, err
	}
	facets := []any{}
	people := map[string]struct{}{}
	type match struct {
		record *Object
		key    []byte
	}
	matching := make([]match, 0, len(records))
	for _, entry := range records {
		record := entry.(*Object)
		scope := value(record, "scope")
		if scope == nil && q.Scene != "all" {
			return nil, errors.New("memory_admin_scope_required")
		}
		properties, _ := scope.(*Object)
		kind, recordScene, recordPerson := value(properties, "kind"), value(properties, "sceneId"), value(properties, "personId")
		sameScene := kind == "scene" && recordScene == scene
		if scene != "" && sameScene && truthy(recordPerson) {
			id, ok := recordPerson.(string)
			if !ok {
				return nil, errors.New("memory_admin_person_required")
			}
			if _, exists := people[id]; !exists {
				people[id] = struct{}{}
				facets = append(facets, id)
			}
		}
		if q.Scene != "all" && !(q.Scene == "public" && kind == "sharedPublicKnowledge") && !sameScene ||
			person != "" && (scene == "" || !sameScene || recordPerson != person) ||
			q.Kind != "all" && value(record, "kind") != q.Kind ||
			q.Status != "all" && value(record, "status") != q.Status {
			continue
		}
		if search != "" {
			content, ok := value(record, "content").(string)
			if !ok {
				return nil, errors.New("memory_admin_content_required")
			}
			lower, err := locale.Lower(content)
			if err != nil {
				return nil, err
			}
			if !strings.Contains(lower, search) {
				continue
			}
		}
		matching = append(matching, match{record: record})
	}
	if len(matching) > 1 {
		for i := range matching {
			at, ok := value(matching[i].record, "learnedAt").(string)
			if !ok {
				return nil, errors.New("memory_admin_learned_at_required")
			}
			matching[i].key, err = locale.SortKey(at)
			if err != nil {
				return nil, err
			}
		}
		slices.SortStableFunc(matching, func(a, b match) int { return bytes.Compare(b.key, a.key) })
	}
	count := len(matching)
	pages := count / q.Limit
	if count%q.Limit != 0 {
		pages++
	}
	page := int(min(q.Page, int64(max(1, pages))))
	start := (page - 1) * q.Limit
	items := make([]any, 0, min(q.Limit, count-start))
	for _, selected := range matching[start:min(start+q.Limit, count)] {
		items = append(items, selected.record)
	}
	result, _ := jsjson.NewObject(
		jsjson.Member{Key: "items", Value: items},
		jsjson.Member{Key: "total", Value: float64(len(records))},
		jsjson.Member{Key: "matched", Value: float64(count)},
		jsjson.Member{Key: "page", Value: float64(page)},
		jsjson.Member{Key: "limit", Value: float64(q.Limit)},
		jsjson.Member{Key: "personIds", Value: facets},
	)
	return jsjson.Marshal(result)
}
