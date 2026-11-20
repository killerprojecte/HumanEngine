package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type budgetAdminOwner struct {
	budget    *providertransport.BudgetService
	publisher *recovery.ReadPublisher
}

func (s *Server) WithBudgetAdmin(budget *providertransport.BudgetService, publisher *recovery.ReadPublisher) error {
	if budget == nil || budget.DB != s.main || budget.Ledger == nil || budget.Ledger.DB != s.main || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_budget_admin_owner_invalid")
	}
	s.budgetAdmin = &budgetAdminOwner{budget, publisher}
	return nil
}

func budgetAdminRead(method, path string) bool {
	return (method == http.MethodGet || method == http.MethodHead) &&
		(path == "/api/budget" || path == "/api/budget/recovery")
}

func (s *Server) serveBudgetAdmin(w http.ResponseWriter, r *http.Request, available bool, path, requestID string) {
	ctx := r.Context()
	head := r.Method == http.MethodHead
	fail := func() {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "budget_admin.failed", Message: "Budget administration read failed", RequestID: requestID})
		}
		if ctx.Err() == nil {
			if head {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
			}
		}
	}
	if !available {
		fail()
		return
	}
	owner := s.budgetAdmin
	response := newPublicationResponse(w.Header(), head)
	readStarted := false
	err := owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		readStarted = true
		if path == "/api/budget/recovery" {
			return owner.budget.ReadBudgetRecovery(ctx, q)
		}
		return owner.budget.ReadBudgetCoverage(ctx, q)
	}, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(ctx, body)
	})
	if response.accepted {
		_ = response.drain(ctx, w)
	} else if err != nil {
		if readStarted {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		fail()
	}
}
