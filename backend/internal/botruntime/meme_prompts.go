package botruntime

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Deliberate P1-B delta from the published legacy behavior. The frozen Runtime
// oracle remains intact; only the named meme policy paragraphs are overlaid.
//
//go:embed testdata/meme-behavior.json
var memeBehaviorJSON []byte
var memeBehavior = func() struct{ Planner, Replyer, ToolDescription string } {
	var value struct{ Planner, Replyer, ToolDescription string }
	if err := json.Unmarshal(memeBehaviorJSON, &value); err != nil {
		panic(err)
	}
	if value.Planner == "" || value.Replyer == "" {
		panic("meme_prompt_delta_missing")
	}
	return value
}()

func applyMemePrompt(value string) string {
	value = strings.Replace(value, "Organize only necessary cited facts, response focus, constraints, and emoji/meme choices. ", "Organize only necessary cited facts, response focus, constraints, and emoji/meme choices. "+memeBehavior.Planner, 1)
	return strings.Replace(value, "Proposed actions are not successful:", memeBehavior.Replyer+"Proposed actions are not successful:", 1)
}
