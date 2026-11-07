package commitments

import "errors"

// Rows is the narrow SQL result contract accepted by a borrowed read scope.
type Rows = rows

// BorrowReadScope borrows the exact owner snapshot without entering recovery,
// creating schemas or exporting a connection. Callbacks must refer to the same
// capability and must reject expiry and suspended write ancestors.
func BorrowReadScope(check, ready func() error, query func(string, ...any) (Rows, error), options Options) (*Reader, error) {
	if check == nil || ready == nil || query == nil {
		return nil, errors.New("commitment_read_scope_required")
	}
	if err := check(); err != nil {
		return nil, err
	}
	return &Reader{check: check, options: options, ready: func() error {
		if err := check(); err != nil {
			return err
		}
		return ready()
	}, query: func(q string, args ...any) (rows, error) {
		if err := check(); err != nil {
			return nil, err
		}
		return query(q, bind(args)...)
	}}, nil
}
func (r *Reader) CheckScope() error {
	if r == nil {
		return errors.New("commitment_reader_required")
	}
	if r.check != nil {
		return r.check()
	}
	return r.ready()
}
