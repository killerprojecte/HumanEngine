package memoryservice

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

func TestActualSourceSemanticNeighborhood(t *testing.T) {
	_, _, _, read := semanticOwnerSetup(t)
	ticket, err := read.Require(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../semantic/testdata/owner-source.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	cases := epArr(value(object(parsed), "neighborhoodCases"))
	if len(cases) != 10 {
		t.Fatal("missing source neighborhood cases", len(cases))
	}
	for _, item := range cases {
		c := object(item)
		scenario := text(c, "scenario")
		t.Run(scenario, func(t *testing.T) {
			views := object(value(c, "views"))
			records := []*Object{}
			for _, v := range epArr(value(c, "records")) {
				records = append(records, object(v))
			}
			current := func(_ Context, ref *Object) (*Object, error) {
				if scenario == "unavailable-member" && text(ref, "id") == "b" {
					return nil, nil
				}
				return object(value(views, text(ref, "id"))), nil
			}
			refs := func(_ Context, rs []*Object) (out []*Object, err error) {
				for _, r := range rs {
					if scenario == "missing-ref" && text(r, "id") == "b" {
						continue
					}
					out = append(out, epObj("id", text(r, "id"), "version", float64(1), "deletionEpoch", float64(0)))
				}
				return
			}
			n := semanticNeighborhood{
				current:  current,
				records:  func(Context) ([]*Object, error) { return records, nil },
				refs:     refs,
				episodes: func(Context, *Object) (*Object, error) { return epObj("groups", []any{value(c, "group")}), nil },
				partition: func(ctx Context, person any) (string, error) {
					filtered := []*Object{}
					for _, r := range records {
						scope := object(value(r, "scope"))
						if !truthy(value(r, "internalUse")) && text(r, "origin") == "learned" && text(scope, "kind") == "scene" && text(scope, "sceneId") == ctx.SceneID && epEqual(value(scope, "personId"), person) {
							filtered = append(filtered, r)
						}
					}
					if len(filtered) < 2 || len(filtered) > 12 {
						return "", nil
					}
					rr, e := refs(ctx, filtered)
					if e != nil {
						return "", e
					}
					selections := []any{}
					for _, ref := range rr {
						a, e := current(ctx, ref)
						if e != nil {
							return "", e
						}
						if a != nil {
							selections = append(selections, epObj("ref", ref, "dependencyKey", value(a, "dependencyKey")))
						}
					}
					if len(selections) != len(filtered) {
						return "", nil
					}
					sort.SliceStable(selections, func(i, j int) bool {
						return epLess(text(object(epGet(selections[i], "ref")), "id"), text(object(epGet(selections[j], "ref")), "id"))
					})
					return semanticHash(selections)
				},
			}
			dirty := []semantic.Dirty{}
			for _, v := range epArr(value(c, "dirty")) {
				d := object(v)
				dirty = append(dirty, semantic.Dirty{SceneID: text(d, "sceneId"), Trigger: object(value(d, "trigger")), EpisodeAnchor: object(value(d, "episodeAnchor")), Reason: text(d, "reason")})
			}
			result, e := n.resolve(dirty, ticket)
			if e != nil {
				t.Fatal(e)
			}
			var got any
			if result != nil {
				ids := []any{}
				for _, entry := range result.Entries {
					ids = append(ids, epGet(value(entry.Selection, "ref"), "id"))
				}
				var partition, membership, anchor any
				if result.PartitionKey != nil {
					partition = *result.PartitionKey
				}
				if result.EpisodeMembershipKey != nil {
					membership = *result.EpisodeMembershipKey
				}
				if result.EpisodeAnchor != nil {
					anchor = result.EpisodeAnchor
				}
				got = epObj("coverage", result.Coverage, "ids", ids, "partitionKey", partition, "episodeAnchor", anchor, "episodeMembershipKey", membership)
			}
			if !epEqual(got, value(c, "expected")) {
				t.Fatal(got, value(c, "expected"))
			}
		})
	}
}
