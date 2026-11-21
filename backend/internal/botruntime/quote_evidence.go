package botruntime

func messageIDs(rows []*Object) map[string]bool {
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[idOf(row)] = true
	}
	return ids
}

func quotedByUsers(rows []*Object) map[string]bool {
	var ids map[string]bool
	for _, row := range rows {
		if id := str(get(row, "replyToId")); id != "" && get(row, "senderId") != "bot" {
			if ids == nil {
				ids = map[string]bool{}
			}
			ids[id] = true
		}
	}
	return ids
}

// Automatic quote evidence never joins the Planner target/context contract or
// the live-window timestamp set. Only an exact retained reference survives the
// draft dependency narrowing.
func (r *Runtime) planQuoteEvidence(c *TurnContext, recent, images []*Object) (quotes, support []*Object, err error) {
	wanted := quotedByUsers(recent)
	if len(wanted) == 0 {
		return nil, nil, nil
	}
	byID := make(map[string]*Object, len(wanted))
	var order []string
	for _, rows := range [][]*Object{images, recent} {
		for _, row := range rows {
			id := idOf(row)
			if !wanted[id] {
				continue
			}
			if byID[id] == nil {
				order = append(order, id)
			}
			byID[id] = row
		}
	}
	inWindow := messageIDs(recent)
	for _, id := range order {
		row := byID[id]
		if get(row, "sceneId") != c.Scene.ID || get(row, "senderId") != "bot" || get(row, "status") != "available" {
			continue
		}
		if recalled, err := r.recalled(c.owner.state, id); err != nil {
			return nil, nil, err
		} else if recalled {
			continue
		}
		valid, err := r.services.Authority.ValidateSources(c.Scene, []*Object{row}, nil)
		if err != nil {
			return nil, nil, err
		}
		if !valid {
			continue
		}
		quotes = append(quotes, row)
		if !inWindow[id] {
			support = append(support, row)
			c.AddDependency(id)
		}
	}
	if len(support) > 0 {
		c.AddScopedValidation(func(scope ReadAuthority) (bool, error) {
			c.mu.Lock()
			active := make([]*Object, 0, len(support))
			for _, row := range support {
				if c.dependencies[idOf(row)] {
					active = append(active, row)
				}
			}
			c.mu.Unlock()
			if len(active) == 0 {
				return true, nil
			}
			return scope.ValidateSources(c.Scene, active, nil)
		})
	}
	return quotes, support, nil
}

func composeQuotedMessages(selected, quotes []*Object) ([]*Object, map[string]string) {
	if len(quotes) == 0 {
		return selected, nil
	}
	wanted := quotedByUsers(selected)
	if len(wanted) == 0 {
		return selected, nil
	}
	var prefix []*Object
	selectedIDs := messageIDs(selected)
	matched := make(map[string]bool)
	for _, quote := range quotes {
		id := idOf(quote)
		if wanted[id] {
			matched[id] = true
			if !selectedIDs[id] {
				prefix = append(prefix, quote)
			}
		}
	}
	references := make(map[string]string)
	for _, row := range selected {
		if quoteID := str(get(row, "replyToId")); get(row, "senderId") != "bot" && matched[quoteID] {
			references[idOf(row)] = quoteID
		}
	}
	if len(prefix) == 0 {
		return selected, references
	}
	return append(prefix, selected...), references
}
