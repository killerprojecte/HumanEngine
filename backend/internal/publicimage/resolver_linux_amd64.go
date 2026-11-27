//go:build cgo

package publicimage

import "github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"

func NewReader(beginWork func() (func(), bool)) *Reader {
	return &Reader{Lookup: pinnedhop.LookupChild, BeginWork: beginWork}
}
