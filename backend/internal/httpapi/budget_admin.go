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
	compare   func(string, string) (int, error)
}

func (s *Server) WithBudgetAdmin(budget *providertransport.BudgetService, publisher *recovery.ReadPublisher, compare func(string, string) (int, error)) error {
	if compare == nil || budget == nil || budget.DB != s.main || budget.Ledger == nil || budget.Ledger.DB != s.main || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_budget_admin_owner_invalid")
	}
	s.budgetAdmin = &budgetAdminOwner{budget, publisher, compare}
	return nil
}

func budgetAdminPath(method, path string) bool {
	switch path {
	case budgetAckPath:
		return true
	case budgetExternalCostsPath:
		return method == http.MethodGet || method == http.MethodHead || method == http.MethodPost
	case "/api/budget", "/api/budget/recovery":
		return method == http.MethodGet || method == http.MethodHead
	default:
		return false
	}
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
		switch path {
		case "/api/budget/recovery":
			return owner.budget.ReadBudgetRecovery(ctx, q)
		case budgetExternalCostsPath:
			page, size, valid := externalCostPage(r.URL.RawQuery)
			if !valid {
				response.status = http.StatusBadRequest
				return []byte(`{"error":"Invalid request"}`), nil
			}
			return owner.budget.ReadExternalCosts(ctx, q, page, size, owner.compare)
		default:
			return owner.budget.ReadBudgetCoverage(ctx, q)
		}
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

func externalCostPage(raw string) (page, size int, valid bool) {
	page, size = 1, 20
	for key, values := range honoQueryValues(raw) {
		if len(values) != 1 {
			return 0, 0, false
		}
		switch key {
		case "page":
			n, ok := coerceQueryInteger(values[0], 10000)
			if !ok {
				return 0, 0, false
			}
			page = int(n)
		case "pageSize":
			n, ok := coerceQueryInteger(values[0], 50)
			if !ok {
				return 0, 0, false
			}
			size = int(n)
		default:
			return 0, 0, false
		}
	}
	return page, size, true
}
