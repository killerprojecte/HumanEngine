package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

const budgetAckPath = "/api/budget/recovery/acknowledge"
const budgetReviewAck = "I_reviewed_accounting_and_accept_incomplete_history"

var budgetAckUUID = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`)

func budgetAcknowledgementID(value any) (string, bool) {
	obj, ok := value.(*jsjson.Object)
	if !ok || obj.Len() != 2 {
		return "", false
	}
	token, _ := obj.Get("acknowledgment")
	id, _ := obj.Get("restoreId")
	s, ok := id.(string)
	return s, ok && token == budgetReviewAck && budgetAckUUID.MatchString(s)
}

func (s *Server) serveBudgetAcknowledgement(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	ctx := r.Context()
	head := r.Method == http.MethodHead
	fail := func(err error, admitted bool) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "budget_admin.failed", Message: err.Error(), RequestID: requestID})
		}
		if ctx.Err() != nil {
			return
		}
		if admitted {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		if head {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
		}
	}
	if !available {
		fail(errors.New("budget_admin_unavailable"), false)
		return
	}
	response := newPublicationResponse(w.Header(), head)
	owner := s.budgetAdmin
	status, literal := 0, ""
	if r.Method != http.MethodPost {
		status, literal = 404, "404 Not Found"
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
	} else {
		// Match source readiness before reading a potentially stalled body.
		if err := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
			fail(err, false)
			return
		}
		if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
			fail(errors.New("budget_ack_json_content_type_required"), true)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			fail(err, true)
			return
		}
		input, err := jsjson.ParseBytes(raw)
		if err != nil {
			fail(err, true)
			return
		}
		restoreID, valid := budgetAcknowledgementID(input)
		if !valid {
			status, literal = 400, `{"error":"Invalid request"}`
		} else {
			err = owner.budget.AcknowledgeBudgetRecovery(ctx, s.authority, restoreID)
			switch {
			case errors.Is(err, providertransport.ErrBudgetRecoveryConflict):
				status, literal = 409, `{"error":"recovery_model_review_conflict"}`
			case err != nil:
				ready := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
				fail(err, ready == nil)
				return
			}
		}
	}
	admitted := false
	err := owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		admitted = true
		if status != 0 {
			response.status = status
			return []byte(literal), nil
		}
		return owner.budget.ReadBudgetRecovery(ctx, q)
	}, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(ctx, body)
	})
	if response.accepted {
		_ = response.drain(ctx, w)
	} else if err != nil {
		fail(err, admitted)
	}
}
