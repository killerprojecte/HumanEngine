package habitservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"regexp"
)

var limited = regexp.MustCompile(`那张|这张|该图|少用它|这次|今天|对我|给我|我个人|this (?:image|meme|sticker|time)|that (?:image|meme|sticker)|for me|personally|today|just kidding|开玩笑|引用|quote`)
var technical = regexp.MustCompile(`技术|technical`)
var verbosity = regexp.MustCompile(`详细|完整|简短|简洁|detail|concise|short repl`)
var verbosityText = regexp.MustCompile(`详细|完整|简短|简洁|短答案|步骤|detail|concise|short`)
var detail = regexp.MustCompile(`详细|完整|detail`)
var verboseAxis = regexp.MustCompile(`简短|简洁|详细|完整|短答案|步骤|concise|detail|short|brief`)
var memeAxis = regexp.MustCompile(`表情包|表情|meme|sticker`)
var emojiAxis = regexp.MustCompile(`emoji|表情`)
var questionAxis = regexp.MustCompile(`问题|提问|反问|question`)
var localScope = regexp.MustCompile(`本群|群里|这个群|in this (?:group|chat)|our group`)
var bot = regexp.MustCompile(`机器人|bot|assistant`)
var generalScope = regexp.MustCompile(`以后|通常|默认|总是|generally|always|by default|from now on`)

// Source regexes use /i without /u: non-ASCII folds must not become ASCII.
func fold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
func supported(p string) bool {
	return p == "concise" || p == "detail" || p == "questions" || p == "emoji" || p == "meme_frequency"
}
func (s *Service) validate(ctx context.Context, c Context, sourcesValue any) (string, error) {
	if c.Simulation {
		return "simulation_read_only", nil
	}
	scene, e := s.scene(ctx, c.SceneID)
	if e != nil {
		return "", e
	}
	if scene == nil || !truthy(get(scene, "learningEnabled")) {
		return "learning_disabled", nil
	}
	sources, ok := sourcesValue.([]any)
	if !ok || len(sources) < 1 || len(sources) > 32 {
		return "invalid_source_evidence", nil
	}
	for _, v := range sources {
		source := object(v)
		if source == nil || !truthy(get(source, "messageId")) || !truthy(get(source, "senderId")) || str(source, "sceneId") != c.SceneID {
			return "invalid_source_evidence", nil
		}
		current, e := s.CurrentSources(ctx, []any{source})
		if e != nil {
			return "", e
		}
		if !current[0] {
			return "invalid_source_evidence", nil
		}
		valid := false
		for _, v := range c.SourceEvidence {
			evidence := object(v)
			if str(evidence, "sceneId") == str(source, "sceneId") && str(evidence, "messageId") == str(source, "messageId") && str(evidence, "senderId") == str(source, "senderId") && truthy(get(evidence, "available")) && !truthy(get(evidence, "isBot")) && str(evidence, "origin") == "live" && finite(get(evidence, "timestamp")) && number(evidence, "timestamp") <= s.now(s)+300000 && number(evidence, "timestamp") > s.now(s)-s.policy.RetentionDays*day {
				valid = true
				break
			}
		}
		if !valid {
			return "invalid_source_evidence", nil
		}
	}
	return "", nil
}
func fingerprint(c Context, input *Object, scene, scopeContext string) (string, error) {
	if str(input, "kind") == "sceneRule" {
		normalized, e := jsstring.FingerprintText(str(input, "content"))
		if e != nil {
			return "", e
		}
		return hash([]any{"scene", c.SceneID, normalized})
	}
	if scene == "" {
		scene = "shared"
	}
	if scopeContext == "" {
		scopeContext = "general"
	}
	return hash([]any{"bot-v2", scene, scopeContext, get(input, "preference")})
}

type prepared struct {
	input                  *Object
	scene, context, reason string
}

func (s *Service) prepare(ctx context.Context, c Context, input *Object) (prepared, error) {
	sources := arr(get(input, "sources"))
	texts := make([]string, len(sources))
	for i, source := range sources {
		texts[i] = fold(str(evidenceFor(c, object(source)), "text"))
		if trim(texts[i]) == "" || limited.MatchString(texts[i]) {
			return prepared{reason: "ambiguous_expression_scope"}, nil
		}
	}
	if str(input, "kind") == "sceneRule" {
		match := technical.MatchString(fold(str(input, "content"))) && verbosity.MatchString(fold(str(input, "content")))
		anyTechnical := false
		for _, text := range texts {
			match = match && verbosityText.MatchString(text)
			anyTechnical = anyTechnical || technical.MatchString(text)
		}
		if match && !anyTechnical {
			records, e := s.AdminList(ctx)
			if e != nil {
				return prepared{}, e
			}
			for _, r := range records {
				if str(r, "sceneId") != c.SceneID || str(r, "context") != "technical" || str(r, "status") != "active" {
					continue
				}
				allAuthors := true
				for _, source := range sources {
					found := false
					for _, prior := range arr(get(r, "sources")) {
						a, e := s.author(ctx, object(prior))
						if e != nil {
							return prepared{}, e
						}
						b, e := s.author(ctx, object(source))
						if e != nil {
							return prepared{}, e
						}
						if a == b {
							found = true
							break
						}
					}
					if !found {
						allAuthors = false
						break
					}
				}
				if allAuthors {
					anyTechnical = true
					break
				}
			}
		}
		if match && anyTechnical {
			direction := float64(1)
			if detail.MatchString(fold(str(input, "content"))) {
				direction = -1
			}
			return prepared{input: obj("kind", "botExpression", "preference", "concise", "direction", direction, "sources", sources), scene: c.SceneID, context: "technical"}, nil
		}
		return prepared{input: input, scene: c.SceneID}, nil
	}
	axis := questionAxis
	switch str(input, "preference") {
	case "concise", "detail":
		axis = verboseAxis
	case "meme_frequency":
		axis = memeAxis
	case "emoji":
		axis = emojiAxis
	}
	tech, local, general, anyBot := false, false, true, false
	for _, text := range texts {
		if !axis.MatchString(text) {
			return prepared{reason: "unsupported_expression_target"}, nil
		}
		tech = tech || technical.MatchString(text)
		local = local || localScope.MatchString(text)
		general = general && bot.MatchString(text) && generalScope.MatchString(text)
		anyBot = anyBot || bot.MatchString(text)
	}
	local = local || tech
	if !local && !general {
		if anyBot {
			local = true
		} else {
			return prepared{reason: "ambiguous_expression_scope"}, nil
		}
	}
	next := input
	if str(input, "preference") == "detail" {
		next = input.Clone()
		_ = next.Set("preference", "concise")
		direction := float64(1)
		if number(input, "direction") == 1 {
			direction = -1
		}
		_ = next.Set("direction", direction)
	}
	result := prepared{input: next}
	if local {
		result.scene = c.SceneID
	}
	if tech {
		result.context = "technical"
	}
	return result, nil
}
func validCandidate(input *Object) bool {
	if str(input, "kind") == "sceneRule" {
		content, ok := get(input, "content").(string)
		units, e := jsjson.UTF16(content)
		return ok && trim(content) != "" && e == nil && len(units) <= 1000
	}
	return str(input, "kind") == "botExpression" && supported(str(input, "preference")) && validDirection(get(input, "direction"))
}
