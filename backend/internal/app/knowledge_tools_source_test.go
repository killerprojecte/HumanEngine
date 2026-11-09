package app

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"testing"
)

func TestKnowledgeToolActualSourceSchema(t *testing.T) {
	raw, err := jsjson.Parse(knowledgeToolsSource)
	if err != nil {
		t.Fatal(err)
	}
	fixture := raw.(*jsjson.Object)
	cases := contextualField(fixture, "queryCases").([]any)
	for i, v := range cases {
		c := v.(*jsjson.Object)
		got, err := knowledge.ParseQuery(contextualField(c, "input"), true)
		want := contextualField(c, "success").(bool)
		if (err == nil) != want {
			t.Fatalf("actual TS query case %d: Go success %v want %v: %v", i, err == nil, want, err)
		}
		if !want {
			continue
		}
		a, err := jsjson.Stringify(got)
		if err != nil {
			t.Fatal(err)
		}
		b, err := jsjson.Stringify(contextualField(c, "data"))
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("actual TS query case %d output: %s want %s", i, a, b)
		}
	}
	d := knowledgeToolDefinition()
	if contextualField(d, "name") != "memory.knowledge" {
		t.Fatal("wrong source tool")
	}
}
