package memeservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func (s *Service) runtimeRead(sources *sourceevents.Store, reads botruntime.ReadAuthority, fn func(sourceevents.RuntimeReadScope, *scope) error) error {
	return sources.UseRuntimeReadScope(reads, func(read sourceevents.RuntimeReadScope) error {
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
		return fn(read, &scope{reader: reader, proof: proof, owner: s})
	})
}

// VisibleInRuntimeScope is the synchronous validation used by send/recall.
// DomainSourceCurrent only uses fixed SELECTs; no session escapes this callback.
func (s *Service) VisibleInRuntimeScope(sources *sourceevents.Store, reads botruntime.ReadAuthority, c Context, id string) (valid bool, err error) {
	err = s.runtimeRead(sources, reads, func(read sourceevents.RuntimeReadScope, p *scope) error {
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
		valid, e = p.visible(c, a, true)
		return e
	})
	return
}

// AnalyzedContentInRuntimeScope selects the same exact-byte asset as recognition.
// A later guard supplies its ID to validate that same output, never a substitute.
func (s *Service) AnalyzedContentInRuntimeScope(sources *sourceevents.Store, reads botruntime.ReadAuthority, c Context, hash, id string) (out *Object, err error) {
	err = s.runtimeRead(sources, reads, func(read sourceevents.RuntimeReadScope, p *scope) error {
		key, value := "contentHash", hash
		if id != "" {
			key, value = "id", id
		}
		query, args := assetCandidatesQuery(c, key, value)
		all, e := decodeAssetRows(read.Query(query, args...))
		if e != nil {
			return e
		}
		match, e := p.matchContent(c, hash, all)
		if e == nil && yes(get(match, "analyzed")) {
			out = match
		}
		return e
	})
	return
}
