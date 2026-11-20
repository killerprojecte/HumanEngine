package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
)

func memoryWriteRoute(method, path string) (string, bool) {
	if method == http.MethodPost {
		return "", path == memoryDeletePrefix
	}
	if method != http.MethodPatch {
		return "", false
	}
	tail, ok := strings.CutPrefix(path, memoryDeletePrefix+"/")
	if !ok || tail == "" || strings.Contains(tail, "/") {
		return "", false
	}
	return habitTryDecode(tail, false), true
}

func (s *Server) patchMemory(ctx context.Context, w http.ResponseWriter, id string, value any, enqueue func([]byte) error) error {
	input, parsed, err := s.schemas.ParseMemoryPatch(value)
	if err != nil {
		habitError(w, 400, "Invalid request", "")
		return nil
	}
	var guard *memorymanual.Guard
	if parsed != nil {
		guard = &memorymanual.Guard{
			ExpectedVersion: parsed.ExpectedVersion, ExpectedDeletionEpoch: parsed.ExpectedDeletionEpoch,
			IdempotencyKey: parsed.IdempotencyKey,
		}
	}
	ack, err := s.memoryDelete.UpdateForAcknowledgement(ctx, id, input, guard)
	if err != nil {
		switch err.Error() {
		case "assertion_version_conflict", "assertion_deleted_or_stale_epoch", "memory_command_conflict", "memory_command_superseded":
			habitError(w, 409, "Memory changed; reload before editing", "")
			return nil
		default:
			return err
		}
	}
	content, _ := ack.Reply.Get("content")
	s.manualMemoryIndex(ctx, id, content.(string))
	return s.memoryAdminRead.PublishUpdated(ctx, id, ack, enqueue)
}
