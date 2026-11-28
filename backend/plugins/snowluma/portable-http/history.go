//go:build wasip1

package main

import (
	"cmp"
	"slices"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func historyAction(in *jsjson.Object) any {
	if id := str(in, "message"); id != "" {
		return map[string]any{"action": "get_msg", "params": map[string]string{"message_id": id}}
	}
	peer := obj(get(in, "peer"))
	action, field := "get_group_msg_history", "group_id"
	if str(peer, "kind") == "private" {
		action, field = "get_friend_msg_history", "user_id"
	}
	return map[string]any{"action": action, "params": map[string]any{field: str(peer, "id"), "count": get(in, "limit"), "reverse_order": true}}
}

func historyResult(in *jsjson.Object) any {
	response := obj(get(in, "response"))
	if get(response, "status") != "ok" || get(response, "retcode") != float64(0) {
		return nil
	}
	data := obj(get(response, "data"))
	single := str(in, "message")
	rows := []any{data}
	if single == "" {
		var ok bool
		rows, ok = get(data, "messages").([]any)
		if !ok {
			return nil
		}
	}
	adapter, account := str(in, "adapter"), str(in, "account")
	out := make([]*jsjson.Object, 0, len(rows))
	for _, item := range rows {
		raw := obj(item)
		id, ok := snow.RawID(get(raw, "message_id"))
		if !ok {
			return nil
		}
		value := normalizedMessage(adapter, account, raw, "history", id)
		if value == nil {
			return nil
		}
		if str(value, "type") == "ignore" {
			continue
		}
		out = append(out, value)
	}
	slices.SortStableFunc(out, func(a, b *jsjson.Object) int {
		return cmp.Compare(str(a, "sentAt"), str(b, "sentAt"))
	})
	unique := out[:0]
	indexes := make(map[string]int, len(out))
	for _, row := range out {
		id := str(row, "message")
		if i, dupe := indexes[id]; dupe {
			unique[i] = row
		} else {
			indexes[id] = len(unique)
			unique = append(unique, row)
		}
	}
	items := make([]any, len(unique))
	for i, row := range unique {
		items[i] = row
	}
	result, _ := jsjson.NewObject(jsjson.Member{Key: "items", Value: items})
	return result
}
