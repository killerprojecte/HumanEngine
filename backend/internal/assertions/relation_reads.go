package assertions

import "github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"

// Relations borrows this exact publication/transaction lifetime. It never
// acquires another publication lock or exposes the underlying connection.
func (r *Reader) Relations() (*relationstorage.Reader, error) {
	return relationstorage.BorrowReadScope(r.check, func(q string, args ...any) (relationstorage.Rows, error) { return r.query(q, args...) })
}
