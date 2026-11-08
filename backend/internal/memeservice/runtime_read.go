package memeservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// VisibleInRuntimeScope is the synchronous validation used by send/recall.
// DomainSourceCurrent only uses fixed SELECTs; no session escapes this callback.
func (s *Service) VisibleInRuntimeScope(sources *sourceevents.Store, reads botruntime.ReadAuthority, c Context, id string) (valid bool, err error) {
	err = sources.UseRuntimeReadScope(reads, func(read sourceevents.RuntimeReadScope) error {
		if e := read.CheckBinding(s.main, s.authority); e != nil {
			return e
		}
		reader, e := assertions.BorrowReadScope(read.Ready, read.Ready, func(q string, args ...any) (assertions.Rows, error) { return read.Query(q, args...) }, func(string) (assertions.PreparedRead, error) {
			return nil, errors.New("meme_fixed_domain_read_has_no_prepared_queries")
		})
		if e != nil {
			return e
		}
		proof, e := assertionauthority.New(reader, s.policy)
		if e != nil {
			return e
		}
		rows, e := read.Query("SELECT value FROM meme_assets WHERE id=?", id)
		if e != nil {
			return e
		}
		var a *Object
		if rows.Next() {
			var raw string
			e = rows.Scan(&raw)
			if e == nil {
				var v any
				v, e = jsjson.Parse(raw)
				a = obj(v)
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		p := scope{reader: reader, proof: proof, owner: s}
		valid, e = p.visible(c, a, true)
		return e
	})
	return
}
