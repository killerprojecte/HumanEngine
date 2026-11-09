package memoryservice

import (
	"reflect"
	"testing"
)

func knowledgeSnapshotFixture() *Object {
	source := func(id string) *Object { return relationObject("messageId", id) }
	proof := func(id string) *Object { return relationObject("sources", []any{source(id)}) }
	ref := relationObject("id", "backing", "version", float64(1), "deletionEpoch", float64(0))
	edge := relationObject("id", "edge", "sceneId", "s", "backing", ref, "proofs", []any{proof("proof-source")}, "spans", []any{relationObject("source", source("span-source"))}, "readDependencies", []any{relationObject("proofs", []any{proof("read-source")})}, "dependencies", []any{relationObject("proofs", []any{proof("dependency-source"), proof("record-source")})})
	record := relationObject("id", "backing", "scope", relationObject("kind", "scene", "sceneId", "s"), "sources", []any{source("record-source")})
	return relationObject("interpretation", "attributed_claims_not_transitive_truth", "coverage", "complete", "edges", []any{edge}, "supportClosure", relationObject("edges", []any{}, "records", []any{relationObject("ref", ref, "record", record)}), "paths", []any{[]any{"edge"}})
}
func TestKnowledgeExposureSnapshotAndSources(t *testing.T) {
	page := knowledgeSnapshotFixture()
	snapshot, err := knowledgeSnapshot("s", 1, page)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := knowledgeSourceIDs([]*Object{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"record-source", "proof-source", "span-source", "read-source", "dependency-source"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ordered source authority %v, want %v", ids, want)
	}
	page.Set("coverage", "limited")
	if text(object(value(snapshot, "page")), "coverage") != "complete" {
		t.Fatal("snapshot retained mutable owner output")
	}
	if _, err = knowledgeSnapshot("foreign", 1, page); err == nil || err.Error() != "knowledge_exposure_page_invalid" {
		t.Fatalf("source scope error lost: %v", err)
	}
}
func TestKnowledgeExposureSnapshotBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Object)
	}{
		{"edge scene", func(p *Object) { value(p, "edges").([]any)[0].(*Object).Set("sceneId", "foreign") }},
		{"record scene", func(p *Object) {
			c := object(value(p, "supportClosure"))
			r := object(value(c, "records").([]any)[0])
			object(value(object(value(r, "record")), "scope")).Set("sceneId", "foreign")
		}},
		{"reference mismatch", func(p *Object) {
			c := object(value(p, "supportClosure"))
			r := object(value(c, "records").([]any)[0])
			object(value(r, "record")).Set("id", "other")
		}},
		{"foreign path", func(p *Object) { p.Set("paths", []any{[]any{"other"}}) }},
		{"path depth", func(p *Object) { p.Set("paths", []any{[]any{"edge", "edge"}}) }},
		{"edge budget", func(p *Object) {
			v := value(p, "edges").([]any)[0]
			edges := make([]any, 41)
			for i := range edges {
				edges[i] = v
			}
			p.Set("edges", edges)
		}},
		{"record budget", func(p *Object) {
			c := object(value(p, "supportClosure"))
			v := value(c, "records").([]any)[0]
			records := make([]any, 129)
			for i := range records {
				records[i] = v
			}
			c.Set("records", records)
		}},
		{"path budget", func(p *Object) {
			paths := make([]any, 65)
			for i := range paths {
				paths[i] = []any{"edge"}
			}
			p.Set("paths", paths)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := knowledgeSnapshotFixture()
			tc.change(p)
			if _, err := knowledgeSnapshot("s", 1, p); err == nil || err.Error() != "knowledge_exposure_page_invalid" {
				t.Fatalf("expected original invalid page error, got %v", err)
			}
		})
	}
}
