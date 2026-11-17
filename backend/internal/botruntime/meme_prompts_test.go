package botruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

// Update only system-message paragraphs in the frozen request oracle, keeping
// every request shape, tool result, source identity and behavioral expectation.
func memeDeltaOracleRequests(requests []any) []any {
	data, err := json.Marshal(requests)
	if err != nil {
		panic(err)
	}
	var result []any
	if err = json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	for _, request := range result {
		row, ok := request.(map[string]any)
		if !ok {
			continue
		}
		messages, _ := row["messages"].([]any)
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok || message["role"] != "system" {
				continue
			}
			if content, ok := message["content"].(string); ok {
				message["content"] = applyMemePrompt(content)
			}
		}
	}
	return result
}

func TestMemePromptDeltaIsExplicitAndSourceBound(t *testing.T) {
	for _, key := range []string{"plan", "replyer"} {
		original := str(get(get(contracts, "prompts"), key))
		actual := prompt(key)
		if original == actual {
			t.Fatalf("%s delta missing", key)
		}
	}
	if !strings.Contains(prompt("plan"), "proactively use memes.search even if the user did not explicitly request an image") || !strings.Contains(prompt("plan"), "independent meaningful text, or noReply") || !strings.Contains(prompt("replyer"), "a media-only reply is valid") {
		t.Fatal("source meme policy missing")
	}
	if prompt("decision") != str(get(get(contracts, "prompts"), "decision")) {
		t.Fatal("unrelated participation policy changed")
	}
}
