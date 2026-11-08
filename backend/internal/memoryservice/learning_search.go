package memoryservice

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"math"
	"sort"
	"strings"
)

type SearchVector struct {
	SpaceID string
	Vector  []float64
	Limit   *int
}

// Search is a fixed source read after support refresh. Ranking precedes final
// Supported filtering, so unsupported rows do not refill the source result limit.
func (s *Service) Search(ctx context.Context, c Context, query, person string, vector *SearchVector) (out []*Object, err error) {
	if !c.Simulation {
		if err = s.RefreshSupport(ctx); err != nil {
			return
		}
	}
	trimmed, e := jsstring.FingerprintText(query)
	if e != nil {
		return nil, e
	}
	terms, e := memorylexical.Terms(query)
	if e != nil {
		return nil, e
	}
	out = []*Object{}
	err = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		var bytes []byte
		space := ""
		limit := 20
		if vector != nil {
			space = vector.SpaceID
			sp, e := r.VectorSpace(space)
			if e != nil {
				return e
			}
			if sp == nil {
				return errors.New("vector_space_not_found")
			}
			bytes, e = EncodeVector(sp, vector.Vector)
			if e != nil {
				return e
			}
			if vector.Limit != nil {
				limit = max(1, min(100, *vector.Limit))
			}
		}
		if c.Simulation && person != "" {
			return nil
		}
		return r.WithSupportReadStatements(func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			initial, e := r.SearchRows(c.SceneID, c.Simulation, person, false, nil, "")
			if e != nil {
				return e
			}
			denied := []string{}
			for _, row := range initial {
				ok, e := a.CurrentApplicable(row.Record)
				if e != nil {
					return e
				}
				if ok && c.Simulation {
					ok, e = a.Supported(row.Record)
					if e != nil {
						return e
					}
				}
				if !ok {
					denied = append(denied, text(row.Record, "id"))
				}
			}
			rows, e := r.SearchRows(c.SceneID, c.Simulation, person, true, denied, space)
			if e != nil {
				return e
			}
			type ranked struct {
				record *Object
				score  float64
				valid  bool
			}
			rank := []ranked{}
			for _, row := range rows {
				lower, e := memorylexical.Lower(row.Content)
				if e != nil {
					return e
				}
				lex := 0.0
				for _, term := range terms {
					if strings.Contains(lower, term) {
						lex++
					}
				}
				if len(terms) > 0 {
					lex /= float64(len(terms))
				}
				sem := 0.0
				if len(row.Embedding) > 0 {
					sem, e = vectorSimilarity(row.Embedding, bytes)
					if e != nil {
						return e
					}
				}
				relevance := math.Max(lex, sem)
				if trimmed != "" && (relevance <= 0 || math.IsNaN(relevance)) {
					continue
				}
				confidence, valid := value(row.Record, "confidence").(float64)
				valid = valid && !math.IsNaN(relevance)
				importance := .1
				switch text(row.Record, "importance") {
				case "low":
					importance = .02
				case "medium":
					importance = .06
				}
				rank = append(rank, ranked{row.Record, relevance*.8 + confidence*.1 + importance, valid})
			}
			sort.SliceStable(rank, func(i, j int) bool {
				a, b := rank[i], rank[j]
				if a.valid != b.valid {
					return a.valid
				}
				if a.valid && a.score != b.score {
					return a.score > b.score
				}
				return text(a.record, "learnedAt") > text(b.record, "learnedAt")
			})
			for _, row := range rank[:min(limit, len(rank))] {
				ok, e := a.Supported(row.record)
				if e != nil {
					return e
				}
				if !ok {
					continue
				}
				record, e := a.Sanitize(row.record)
				if e != nil {
					return e
				}
				if row.valid {
					_ = record.Set("score", row.score)
				} else {
					_ = record.Set("score", nil)
				}
				out = append(out, record)
			}
			return nil
		})
	})
	return
}
func EncodeVector(space *Object, vector []float64) ([]byte, error) {
	dimensions, _ := value(space, "dimensions").(float64)
	if float64(len(vector)) != dimensions {
		return nil, errors.New("invalid_vector")
	}
	norm := 1.0
	nonzero := false
	for _, v := range vector {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("invalid_vector")
		}
		nonzero = nonzero || v != 0
	}
	if !nonzero {
		return nil, errors.New("invalid_vector")
	}
	if value(space, "normalized") == true {
		largest := 0.0
		for _, v := range vector {
			largest = math.Max(largest, math.Abs(v))
		}
		sum, compensation := 0.0, 0.0
		for _, v := range vector {
			normalized := v / largest
			summand := normalized*normalized - compensation
			next := sum + summand
			compensation = (next - sum) - summand
			sum = next
		}
		norm = math.Sqrt(sum) * largest
	}
	out := make([]byte, 4*len(vector))
	for i, v := range vector {
		x := float32(v / norm)
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			return nil, errors.New("invalid_vector")
		}
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out, nil
}
func vectorSimilarity(a, b []byte) (float64, error) {
	if len(a) != len(b) || len(a) == 0 || len(a)%4 != 0 {
		return 0, errors.New("invalid_vector")
	}
	var dot, aa, bb float32
	for i := 0; i < len(a); i += 4 {
		x := math.Float32frombits(binary.LittleEndian.Uint32(a[i:]))
		y := math.Float32frombits(binary.LittleEndian.Uint32(b[i:]))
		dot += float32(x * y)
		aa += float32(x * x)
		bb += float32(y * y)
	}
	// sqlite-vec accumulates float32 products and returns a float32 distance;
	// SQLite's 1.0-distance conversion then happens at double precision.
	distance := float32(1 - float64(dot)/(math.Sqrt(float64(aa))*math.Sqrt(float64(bb))))
	return math.Max(0, 1-float64(distance)), nil
}

// SearchInSession preserves embedding generation CAS and final search under the
// same real writer snapshot without holding that snapshot during model work.
func (s *Service) SearchInSession(ctx context.Context, ws *recovery.WriteSession, c Context, query, person string, vector *SearchVector) ([]*Object, error) {
	copy := *s
	copy.d = s.d
	copy.d.Assertions = s.d.Assertions.WithSession(ws)
	copy.revisions = s.revisions.WithSession(ws)
	return copy.Search(ctx, c, query, person, vector)
}
