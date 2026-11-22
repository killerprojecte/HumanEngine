package visioncontext

import (
	"context"
	"slices"
	"testing"
)

func TestAdmissionQualifiesOnlyBudgetedOriginals(t *testing.T) {
	var rows []*Message
	var priority []string
	for _, id := range []string{"missing", "one", "two", "three", "four", "over-budget"} {
		priority = append(priority, id)
		rows = append(rows, &Message{ID: id, SceneID: "scene", Status: "available",
			Parts: []Part{{Type: "media", MediaType: "image", AssetID: id}}})
	}
	var qualified, dependencies, guards []string
	p := Ports{
		SourceAvailable: func(Candidate) (bool, error) { return true, nil },
		Admit: func(_ context.Context, c Candidate) (bool, error) {
			qualified = append(qualified, c.Message.ID)
			return c.Message.ID != "missing", nil
		},
		ReadImage: func(context.Context, Candidate) ([]byte, error) {
			t.Fatal("stored label reread media")
			return nil, nil
		},
		ImageDataURL: func(context.Context, []byte) (string, error) { return "", nil },
		Complete: func(context.Context, []ChatMessage) (string, error) {
			t.Fatal("stored label called VLM")
			return "", nil
		},
		AddDependency:       func(id string) { dependencies = append(dependencies, id) },
		AddSourceValidation: func(Candidate) {},
	}
	lib := &Library{
		Lookup:              func(c Candidate) (string, bool, error) { return c.Message.ID, true, nil },
		AddLookupValidation: func(c Candidate, _ string) { guards = append(guards, c.Message.ID) },
	}
	result, err := Describe(t.Context(), new(Turn), Config{SceneID: "scene", Mode: "real", PriorityIDs: priority}, rows, nil, p, lib)
	if err != nil || !slices.Equal(qualified, priority[:5]) || !slices.Equal(dependencies, priority[1:5]) ||
		!slices.Equal(guards, priority[1:5]) || len(result.Images) != 4 ||
		len(result.Omitted) != 1 || result.Omitted[0].MessageID != "over-budget" {
		t.Fatalf("unexpected qualification: %+v guards %v deps %v read %v err %v", result, guards, dependencies, qualified, err)
	}
}
