package externaladapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type oneBotHistoryRequest struct {
	Adapter string `json:"adapter"`
	Account string `json:"account"`
	Peer    peer   `json:"peer"`
	Limit   int    `json:"limit,omitempty"`
	Message string `json:"message,omitempty"`
}

func (s *httpSession) queryOneBotHistory(ctx context.Context, scene dispatch.SceneRef, limit int, id string, before func(context.Context) error, settle func(*jsjson.Object, error)) {
	done, ok := s.work.Start()
	if !ok {
		settle(historyPage("unavailable", "adapter_unavailable", "", []any{}), nil)
		return
	}
	go func() {
		defer done()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		detach := context.AfterFunc(s.life, cancel)
		defer detach()
		page, err := s.readOneBotHistory(ctx, scene, limit, id, before)
		settle(page, err)
	}()
}

func (s *httpSession) readOneBotHistory(ctx context.Context, scene dispatch.SceneRef, limit int, id string, before func(context.Context) error) (*jsjson.Object, error) {
	p := peer{Kind: scene.Kind, ID: scene.PlatformSceneID}
	known, err := s.profile.scene(p)
	if err != nil || scene.ID != known.ID || scene.AdapterID != known.AdapterID || scene.AccountID != known.AccountID {
		return nil, errors.New("external_adapter_history_scope_mismatch")
	}
	input := oneBotHistoryRequest{Adapter: known.AdapterID, Account: known.AccountID, Peer: p, Limit: limit}
	actionName, targetField, target := "get_group_msg_history", "group_id", p.ID
	if p.Kind == "private" {
		actionName, targetField = "get_friend_msg_history", "user_id"
	}
	if id != "" {
		input.Message, err = platformMessage(known, id)
		if err != nil {
			return nil, err
		}
		actionName, targetField, target = "get_msg", "message_id", input.Message
	}
	plan, err := s.invoke(ctx, "adapterHistoryAction", input)
	if err != nil {
		return nil, err
	}
	action, err := oneBotActionEnvelope(plan)
	if err != nil || action.Action != actionName || len(action.Originals) != 0 {
		return nil, errors.New("external_adapter_history_action_invalid")
	}
	params, err := guestObject(action.Params)
	if err != nil || text(params, targetField) != target {
		return nil, errors.New("external_adapter_history_target_invalid")
	}
	reply, err := s.oneBotRead(ctx, actionName, action.Params, before)
	if err != nil {
		if err == errOneBotRead {
			return historyPage("unavailable", "request_failed", "", []any{}), nil
		}
		return nil, err
	}
	answer, err := s.invoke(ctx, "adapterHistoryResult", struct {
		oneBotHistoryRequest
		Response json.RawMessage `json:"response"`
	}{input, reply})
	if err != nil {
		return nil, err
	}
	output, err := guestObject(answer)
	if err != nil {
		return historyPage("unavailable", "invalid_history_response", "", []any{}), nil
	}
	items, _ := output.Get("items")
	rows, valid := items.([]any)
	if !valid || (id != "" && len(rows) != 1) {
		return historyPage("unavailable", "invalid_history_response", "", []any{}), nil
	}
	out := make([]any, 0, len(rows))
	received := time.Now()
	for _, row := range rows {
		object, _ := row.(*jsjson.Object)
		if object == nil {
			return nil, errors.New("external_adapter_history_row_invalid")
		}
		event, err := s.profile.projectParsedEvent(object, true)
		if err != nil || event.Type != "message" || event.Peer != p || (id != "" && input.Message != event.Message) {
			return historyPage("unavailable", "history_scope_mismatch", "", []any{}), nil
		}
		message, err := s.profile.messageWithOrigin(known, event, received, "history")
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	if id != "" {
		return historyPage("complete", "", "", out), nil
	}
	if len(rows) == 0 {
		return historyPage("partial", "history_exhausted_or_unavailable", "", out), nil
	}
	// Take the cursor before the runtime's existing scene-scoped recall
	// filter, so a page of recalled messages still advances.
	first := text(rows[0].(*jsjson.Object), "message")
	return historyPage("partial", "platform_history_window", messageID(known, first), out), nil
}

func historyPage(coverage, reason, next string, rows []any) *jsjson.Object {
	out, _ := jsjson.NewObject(jsjson.Member{Key: "items", Value: rows}, jsjson.Member{Key: "coverage", Value: coverage})
	if reason != "" {
		_ = out.Set("reason", reason)
	}
	if next != "" {
		_ = out.Set("nextCursor", next)
	}
	return out
}
