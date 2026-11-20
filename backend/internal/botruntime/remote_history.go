package botruntime

import (
	"context"
	"slices"
)

// BrowserHistoryWindow bounds each side of the 60-row browser history merge.
const BrowserHistoryWindow = 30

// HistoryPage carries a platform result across the request's authority fence.
// Its rows are not published until locally reconciled.
type HistoryPage struct {
	result  *Object
	adapter *Adapter
}

// FetchRemoteHistory contacts the same adapter used by the model history tools.
// No scene or authority lock is held while the request is pending.
func (r *Runtime) FetchRemoteHistory(ctx context.Context, scene Scene) (HistoryPage, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return HistoryPage{}, code("runtime_closed")
	}
	defer done()
	value, ok := r.adapters.Load(scene.AdapterID)
	if !ok {
		return HistoryPage{}, nil
	}
	adapter := value.(*Adapter)
	if adapter.History == nil {
		return HistoryPage{}, nil
	}
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	page, err := r.adapterHistory(ctx, adapter, scene, BrowserHistoryWindow)
	if err != nil {
		return HistoryPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	return HistoryPage{result: page, adapter: adapter}, nil
}

type BrowserHistory struct {
	Items              []*Object
	ImageLinks         map[*Object]DisplayImageLinks
	Coverage           string
	Reason, NextCursor *string
}

// BrowserRemoteHistory uses the current local view, never one captured before
// waiting on the platform. Its caller fences the materialization for publication.
func (r *Runtime) BrowserRemoteHistory(scene Scene, page HistoryPage, mediaEnabled bool) (BrowserHistory, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return BrowserHistory{}, code("runtime_closed")
	}
	defer done()

	result := BrowserHistory{Coverage: "partial"}
	var remote []*Object
	current, _ := r.adapters.Load(scene.AdapterID)
	unavailable := page.adapter == nil || page.adapter != current
	if unavailable {
		reason := "adapter_unavailable"
		result.Reason = &reason
	} else {
		if value, ok := get(page.result, "reason").(string); ok {
			result.Reason = &value
		}
		if value, ok := get(page.result, "nextCursor").(string); ok {
			result.NextCursor = &value
		}
		if get(page.result, "coverage") == "unavailable" {
			result.Coverage = "unavailable"
		} else if values := array(get(page.result, "items")); len(values) != 0 {
			messages := make([]*Object, len(values))
			for i, value := range values {
				message, ok := value.(*Object)
				if !ok {
					return BrowserHistory{}, code("invalid_adapter_history")
				}
				messages[i] = message
			}
			var recalled map[string]bool
			if s := r.state(scene.ID); s != nil {
				recalled = s.view.Load().recalled
			}
			var err error
			remote, err = r.services.Authority.ProjectRemoteHistory(scene, messages, recalled, page.adapter.BotUserID)
			if err != nil {
				return BrowserHistory{}, err
			}
		}
	}
	local, links, err := r.LocalHistory(scene.ID, mediaEnabled)
	if err != nil {
		return BrowserHistory{}, err
	}
	result.ImageLinks = links
	if unavailable {
		// The no-adapter source path keeps local retention order and skips merge.
		result.Items = availableHistory(local)
		return result, nil
	}
	rows, err := r.mergeBrowserHistory(remote, local)
	if err != nil {
		return BrowserHistory{}, err
	}
	result.Items = availableHistory(rows)
	return result, nil
}

func availableHistory(rows []*Object) []*Object {
	return slices.DeleteFunc(rows, func(row *Object) bool { return get(row, "status") == "recalled" })
}

func (r *Runtime) mergeBrowserHistory(remote, local []*Object) ([]*Object, error) {
	rows := make([]*Object, 0, len(remote)+len(local))
	positions := make(map[string]int, cap(rows))
	appendPage := func(page []*Object) {
		for _, row := range page {
			id := idOf(row)
			if index, exists := positions[id]; exists {
				rows[index] = row
			} else {
				positions[id] = len(rows)
				rows = append(rows, row)
			}
		}
	}
	appendPage(remote)
	appendPage(local)
	// Date.parse at the V1 SDK boundary accepts some non-strings, while the
	// source browser response requires strings and sorts by localeCompare.
	type entry struct {
		row    *Object
		sentAt string
	}
	ordered := make([]entry, len(rows))
	for i, row := range rows {
		at, ok := get(row, "sentAt").(string)
		if !ok {
			return nil, code("invalid_history_timestamp")
		}
		ordered[i] = entry{row: row, sentAt: at}
	}
	var compareErr error
	slices.SortStableFunc(ordered, func(a, b entry) int {
		order, err := r.services.Compare(a.sentAt, b.sentAt)
		if err != nil {
			compareErr = err
		}
		return order
	})
	if compareErr != nil {
		return nil, compareErr
	}
	for i, item := range ordered {
		rows[i] = item.row
	}
	return rows, nil
}
