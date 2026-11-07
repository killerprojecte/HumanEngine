package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"strings"
)

func (r *Runtime) prepareTurn(t *turn, f *turnFrame, issues *int) error {
	c, s, cfg := t.context, t.state, t.settings
	if valid, err := r.eligible(c); err != nil {
		return err
	} else if !valid {
		t.result.Status = "cancelled"
		t.result.Reason = "context_ineligible"
		return nil
	}
	s.mu.Lock()
	if c.Mode == "real" {
		r.trimLocked(s, false)
	}
	t.version = t.topic.version.Load()
	for body, record := range s.byBody {
		t.records[body] = record
	}
	admitted := map[string]bool(nil)
	if cfg.TopicAdmissionEnabled {
		admitted = map[string]bool{}
		for _, id := range t.topic.messageIDs {
			if _, deferred := s.routing.Deferred.Get(id); !deferred {
				admitted[id] = true
			}
		}
		for _, e := range s.routing.Acknowledged.Entries() {
			if e.Value == t.topic.id {
				admitted[e.Key] = true
			}
		}
	}
	candidates := filterMessages(s.messages, func(m *Object) bool {
		return get(m, "status") == "available" && !s.recalled[idOf(m)] && (admitted == nil || admitted[idOf(m)])
	})
	r.publishLocked(s)
	s.mu.Unlock()
	candidates, readErr := r.unrecalled(s, candidates)
	if readErr != nil {
		return readErr
	}
	extensions := r.exts()
	allTools := []*Object{contractTool("messages.send"), contractTool("messages.get"), contractTool("messages.around"), contractTool("messages.byUser")}
	extensionTools := []*Object{}
	for _, e := range extensions {
		allTools = append(allTools, e.Tools...)
		extensionTools = append(extensionTools, e.Tools...)
	}
	var authority any
	var err error
	if cfg.PromptContextWindowEnabled {
		authority, err = r.promptAuthority(t, extensionTools)
		if err != nil {
			return err
		}
		authorityText, err := jsjson.Stringify(authority)
		if err != nil {
			return err
		}
		c.AddScopedValidation(func(scope ReadAuthority) (bool, error) {
			current, e := r.promptAuthorityUsing(t, scope)
			if e != nil {
				return false, e
			}
			text, e := jsjson.Stringify(current)
			return text == authorityText, e
		})
		if c.Mode == "real" {
			c.AddValidation(func() bool {
				c.mu.Lock()
				sourceTimes := append([]float64{}, c.sourceTimes...)
				c.mu.Unlock()
				cutoff := r.services.Clock.Now() - r.lifetime(cfg)
				for _, at := range sourceTimes {
					if !(at >= cutoff) {
						return false
					}
				}
				return true
			})
		}
	}
	window := cfg.Window
	window.Enabled = cfg.PromptContextWindowEnabled && cfg.CacheEnabled
	participationKey, _ := jsjson.Stringify([]any{c.Mode, c.Scene.ID, t.topic.id, "participation"})
	recent, err := r.selectWindow(participationKey, candidates, window, authority, nil)
	if err != nil {
		return err
	}
	c.setSources(recent)
	c.AddScopedValidation(func(scope ReadAuthority) (bool, error) {
		c.mu.Lock()
		captured := append([]*Object{}, c.sources...)
		c.mu.Unlock()
		return scope.ValidateSources(c.Scene, captured, nil)
	})
	manual := []string{}
	learned := []any{}
	for _, e := range extensions {
		p := ""
		if e.ContextPrompt != nil {
			p, err = e.ContextPrompt(c)
			if err != nil {
				return err
			}
		}
		manual = append(manual, p)
		if e.ContextData != nil {
			data, e := e.ContextData(c)
			if e != nil {
				return e
			}
			if truthy(data) {
				learned = append(learned, data)
			}
		}
	}
	manualContext := strings.Join(manual, "\n")
	projection := replyplan.NewContextProjection(c.Scene.ID)
	if cfg.PromptContextWindowEnabled {
		for _, m := range recent {
			if _, err := projection.Project(obj("evidenceId", "message:"+idOf(m), "value", m)); err != nil {
				return err
			}
		}
	}
	if _, err = projection.Project(objects(recent)); err != nil {
		return err
	}
	imageContext := []any{}
	s.mu.Lock()
	topicIDs := append([]string{}, t.topic.messageIDs...)
	s.mu.Unlock()
	current := filterMessages(recent, func(m *Object) bool { return contains(topicIDs, idOf(m)) })
	priority := []string{}
	missingQuotes := []string{}
	for i := len(current) - 1; i >= 0; i-- {
		id := str(get(current[i], "replyToId"))
		if id != "" {
			priority = append(priority, id)
			if !contains(ids(recent), id) {
				missingQuotes = append(missingQuotes, id)
			}
		}
	}
	for i := len(current) - 1; i >= 0; i-- {
		priority = append(priority, idOf(current[i]))
	}
	for i := len(recent) - 1; i >= 0; i-- {
		priority = append(priority, idOf(recent[i]))
	}
	c.ImagePriorityIDs = unique(priority)
	images := append([]*Object{}, recent...)
	if c.Mode == "real" && len(missingQuotes) > 0 {
		requested := unique(missingQuotes)
		if len(requested) > 4 {
			requested = requested[:4]
		}
		output, e := r.read(t, c.root, "messages.get", obj("ids", stringArray(requested)))
		if e != nil {
			return e
		}
		for _, m := range array(get(output, "items")) {
			if o, ok := m.(*Object); ok {
				images = append(images, o)
			}
		}
	}
	hasImages := false
	for _, m := range images {
		for _, p := range array(get(m, "parts")) {
			media := str(get(p, "mediaType"))
			hasImages = hasImages || get(p, "type") == "media" && (media == "image" || strings.HasPrefix(strings.ToLower(media), "image/"))
		}
	}
	if hasImages {
		for _, e := range extensions {
			if valid, err := r.eligible(c); err != nil {
				return err
			} else if !valid {
				t.result.Status = "cancelled"
				t.result.Reason = "context_cancelled"
				return nil
			}
			if e.DescribeImages != nil {
				out, err := replyplan.WithinSignal(c.root, func(settle func(any, error)) { e.DescribeImages(c.root, c, images, settle) })
				if err != nil {
					return err
				}
				if truthy(out) {
					imageContext = append(imageContext, out)
				}
			}
		}
	}
	if valid, err := r.eligible(c); err != nil {
		return err
	} else if !valid {
		t.result.Status = "cancelled"
		t.result.Reason = "context_cancelled"
		return nil
	}
	learned = append(learned, imageContext...)
	extensionDependencies := c.dependencyIDs()
	c.setDependencies(extensionDependencies, recent)
	f.extensions = extensions
	f.allTools = allTools
	f.extensionTools = extensionTools
	f.authority = authority
	f.admitted = admitted
	f.window = window
	f.participationKey = participationKey
	f.recent = recent
	f.manualContext = manualContext
	f.projection = projection
	f.imageContext = imageContext
	f.learned = learned
	f.extensionDependencies = extensionDependencies
	f.proceed = true
	return nil
}
