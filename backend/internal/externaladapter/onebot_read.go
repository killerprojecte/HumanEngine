package externaladapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errOneBotRead = errors.New("onebot_action_failed")

func (s *httpSession) oneBotRead(ctx context.Context, action string, payload []byte, before func(context.Context) error) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.profile.action, "/")+"/"+action, bytes.NewReader(payload))
	if err != nil {
		return nil, errOneBotRead
	}
	req.Header.Set("Authorization", "Bearer "+s.profile.onebotHTTP.token)
	req.Header.Set("Content-Type", "application/json")
	if before != nil {
		if err := before(ctx); err != nil {
			return nil, err
		}
	}
	if s.life.Err() != nil {
		return nil, errOneBotRead
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, errOneBotRead
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errOneBotRead
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, oneBotActionResponseLimit+1))
	if err != nil || len(raw) > oneBotActionResponseLimit {
		return nil, errOneBotRead
	}
	return raw, nil
}
