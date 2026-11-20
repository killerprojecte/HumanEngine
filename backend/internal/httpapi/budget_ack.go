package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const budgetExternalCostsPath = "/api/budget/external-costs"
const budgetExternalCostCorrectionsPath = budgetExternalCostsPath + "/corrections"
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

func (s *Server) serveBudgetWrite(w http.ResponseWriter, r *http.Request, available bool, path, requestID string) {
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
	var imported externalcost.Receipt
	var observed recovery.Head
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
		if path == budgetExternalCostsPath || path == budgetExternalCostCorrectionsPath {
			var invalid bool
			if path == budgetExternalCostsPath {
				parsed, parseErr := externalcost.ParseImport(input)
				invalid = parseErr != nil
				if !invalid {
					imported, observed, err = owner.budget.ImportExternalCost(ctx, s.authority, parsed)
				}
			} else {
				parsed, parseErr := externalcost.ParseCorrection(input)
				invalid = parseErr != nil
				if !invalid {
					imported, observed, err = owner.budget.CorrectExternalCost(ctx, s.authority, parsed)
				}
			}
			switch {
			case invalid:
				status, literal = 400, `{"error":"Invalid request"}`
			case errors.Is(err, externalcost.ErrConflict):
				status, literal = 409, `{"error":"external_cost_conflict"}`
			case errors.Is(err, externalcost.ErrRevisionConflict):
				status, literal = 409, `{"error":"external_cost_revision_conflict"}`
			case errors.Is(err, externalcost.ErrCapacity):
				status, literal = 409, `{"error":"external_cost_capacity"}`
			case err != nil:
				ready := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
				fail(err, ready == nil)
				return
			}
		} else if restoreID, valid := budgetAcknowledgementID(input); !valid {
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
		if observed != (recovery.Head{}) {
			gate, err := recovery.ReadGate(ctx, q)
			if err != nil {
				return nil, err
			}
			if gate.Head != observed {
				return nil, errors.New("recovery_authority_head_changed")
			}
		}
		if status != 0 {
			response.status = status
			return []byte(literal), nil
		}
		if path == budgetExternalCostsPath || path == budgetExternalCostCorrectionsPath {
			return imported.MarshalJSON()
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
