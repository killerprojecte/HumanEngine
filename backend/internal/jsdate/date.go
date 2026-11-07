// Copyright 2026 HumanEngine contributors.
// Parser adapted from V8's BSD-licensed src/date/dateparser{,-inl}.h and
// dateparser.cc in Node v24.22.1. See LICENSE.v8.
package jsdate

import (
	"fmt"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Parser supplies V8's local-time conversion for strings without a timezone.
// localToUTC takes local epoch milliseconds, not a UTC instant. It must use
// the source runtime's timezone and disambiguation rules. The UTC parser is exact
// when the source process runs in UTC.
type Parser struct{ localToUTC func(int64) int64 }

var utcParser = Parser{localToUTC: func(ms int64) int64 { return ms }}

// FiniteDateParse reports Number.isFinite(Date.parse(value)) in a UTC runtime.
func FiniteDateParse(value string) bool { return utcParser.FiniteDateParse(value) }

func (policy Parser) FiniteDateParse(value string) bool {
	units, err := jsjson.UTF16(value)
	if err != nil {
		return false
	}
	_, valid := policy.parseMillisUnits(units)
	return valid
}

// ParseMillis returns the exact source Date.parse epoch milliseconds. Invalid
// dates return valid=false, corresponding to JavaScript NaN, not an exception.
// Errors describe an unconfigured parser or a noncanonical Go string, neither
// of which can be a JavaScript Date.parse string input. A valid result always
// fits both int64 and JavaScript's exact integer range.
func (p *Parser) ParseMillis(value string) (milliseconds int64, valid bool, err error) {
	if p == nil || p.localToUTC == nil {
		return 0, false, fmt.Errorf("jsdate: unconfigured parser")
	}
	units, err := jsjson.UTF16(value)
	if err != nil {
		return 0, false, err
	}
	milliseconds, valid = p.parseMillisUnits(units)
	return milliseconds, valid, nil
}

func (policy Parser) parseMillisUnits(units []uint16) (int64, bool) {
	p := machine{scan: scanner{units: units}, month: -1, hourOffset: -1, tzSign: 0, tzHour: -1, tzMinute: -1}
	p.scan.next = p.scan.read()
	t := p.iso()
	if t.kind == invalid {
		return 0, false
	}
	seen := len(p.day) > 0
	for ; t.kind != end; t = p.scan.take() {
		if t.kind == number {
			seen = true
			n := t.value
			if p.scan.skip(':') {
				if p.scan.skip(':') {
					if len(p.clock) != 0 {
						return 0, false
					}
					p.addTime(n)
					p.addTime(0)
				} else {
					if !p.addTime(n) {
						return 0, false
					}
					p.scan.skip('.')
				}
			} else if p.scan.skip('.') && p.expectTime(n) {
				p.addTime(n)
				if p.scan.next.kind != number {
					return 0, false
				}
				p.finalTime(milliseconds(p.scan.take()))
			} else if p.tzHour != -1 && p.tzMinute == -1 && between(n, 0, 59) {
				p.tzMinute = n
			} else if p.expectTime(n) {
				p.finalTime(n)
				q := p.scan.next
				if q.kind != end && q.kind != space && !q.z() && !q.sign() {
					return 0, false
				}
			} else {
				if len(p.day) == 3 {
					return 0, false
				}
				p.day = append(p.day, n)
				p.scan.skip('-')
			}
		} else if t.kind >= keyword {
			switch {
			case t.kind == ampm && len(p.clock) > 0:
				p.hourOffset = t.value
			case t.kind == month:
				p.month = t.value
				p.scan.skip('-')
			case t.kind == zone && seen:
				p.zone(t.value)
			default:
				if seen || p.scan.next.kind == number {
					return 0, false
				}
			}
		} else if t.sign() && ((p.tzHour == 0 && p.tzMinute == 0) || len(p.clock) > 0) {
			p.tzSign = 44 - t.value
			n, l := 0, 0
			if p.scan.next.kind == number {
				q := p.scan.take()
				n, l = q.value, q.length
			}
			seen = true
			if p.scan.next.symbol(':') {
				p.tzHour = n
				p.tzMinute = -1
			} else if l == 1 || l == 2 {
				p.tzHour = n
				p.tzMinute = 0
			} else if l == 3 || l == 4 {
				p.tzHour = n / 100
				p.tzMinute = n % 100
			} else {
				return 0, false
			}
		} else if (t.sign() || t.symbol(')')) && seen {
			return 0, false
		}
	}
	if len(p.day) == 0 {
		return 0, false
	}
	for len(p.day) < 3 {
		p.day = append(p.day, 1)
	}
	y, m, d := 0, 0, 0
	if p.month == -1 {
		if p.isISO || !between(p.day[0], 1, 31) {
			y, m, d = p.day[0], p.day[1], p.day[2]
		} else {
			m, d, y = p.day[0], p.day[1], p.day[2]
		}
	} else {
		m = p.month
		if !between(p.day[0], 1, 31) {
			y, d = p.day[0], p.day[1]
		} else {
			d, y = p.day[0], p.day[1]
		}
	}
	if !p.isISO {
		if between(y, 0, 49) {
			y += 2000
		} else if between(y, 50, 99) {
			y += 1900
		}
	}
	if !between(m, 1, 12) || !between(d, 1, 31) {
		return 0, false
	}
	for len(p.clock) < 4 {
		p.clock = append(p.clock, 0)
	}
	h, mi, s, ms := p.clock[0], p.clock[1], p.clock[2], p.clock[3]
	if p.hourOffset != -1 {
		if !between(h, 0, 12) {
			return 0, false
		}
		h = h%12 + p.hourOffset
	}
	if !(between(h, 0, 23) && between(mi, 0, 59) && between(s, 0, 59) && between(ms, 0, 999)) && !(h == 24 && mi == 0 && s == 0 && ms == 0) {
		return 0, false
	}
	// V8 MakeDay rejects years outside this intrinsic arithmetic range.
	if y < -1000000 || y > 1000000 {
		return 0, false
	}
	date := time.Date(y, time.Month(m), d, h, mi, s, ms*1000000, time.UTC).UnixMilli()
	if p.tzSign != 0 {
		th, tm := p.tzHour, p.tzMinute
		if th == -1 {
			th = 0
		}
		if tm == -1 {
			tm = 0
		}
		offset := uint32(th)*3600 + uint32(tm)*60
		if offset > 2147483647 {
			return 0, false
		}
		date -= int64(p.tzSign) * int64(offset) * 1000
	} else {
		const beforeUTC = int64(8640000000000000 + 86400000*30)
		if date < -beforeUTC || date > beforeUTC || policy.localToUTC == nil {
			return 0, false
		}
		date = policy.localToUTC(date)
	}
	if date < -8640000000000000 || date > 8640000000000000 {
		return 0, false
	}
	return date, true
}

type machine struct {
	scan                                        scanner
	day, clock                                  []int
	month, hourOffset, tzSign, tzHour, tzMinute int
	isISO                                       bool
}

func (p *machine) addTime(n int) bool {
	if len(p.clock) == 4 {
		return false
	}
	p.clock = append(p.clock, n)
	return true
}
func (p *machine) finalTime(n int) {
	if p.addTime(n) {
		for len(p.clock) < 4 {
			p.clock = append(p.clock, 0)
		}
	}
}
func (p *machine) expectTime(n int) bool {
	l := len(p.clock)
	return (l == 1 || l == 2) && between(n, 0, 59) || l == 3 && between(n, 0, 999)
}
func (p *machine) zone(n int) {
	p.tzSign = 1
	if n < 0 {
		p.tzSign = -1
	}
	p.tzHour = n * p.tzSign
	p.tzMinute = 0
}
func between(n, a, b int) bool { return n >= a && n <= b }
func milliseconds(t token) int {
	n, l := t.value, t.length
	if l == 1 {
		return n * 100
	}
	if l == 2 {
		return n * 10
	}
	if l > 9 {
		l = 9
	}
	for l > 3 {
		n /= 10
		l--
	}
	return n
}
func (p *machine) iso() token {
	s := &p.scan
	if s.next.sign() {
		sign := s.take()
		if !s.next.fixed(6) {
			return sign
		}
		y := s.take().value
		if sign.value == '-' && y == 0 {
			return sign
		}
		p.day = append(p.day, (44-sign.value)*y)
	} else if s.next.fixed(4) {
		p.day = append(p.day, s.take().value)
	} else {
		return s.take()
	}
	if s.skip('-') {
		if !s.next.fixed(2) || !between(s.next.value, 1, 12) {
			return s.take()
		}
		p.day = append(p.day, s.take().value)
		if s.skip('-') {
			if !s.next.fixed(2) || !between(s.next.value, 1, 31) {
				return s.take()
			}
			p.day = append(p.day, s.take().value)
		}
	}
	bad := token{kind: invalid}
	if s.next.kind != separator {
		if s.next.kind != end {
			return s.take()
		}
	} else {
		s.take()
		if !s.next.fixed(2) || !between(s.next.value, 0, 24) {
			return bad
		}
		is24 := s.next.value == 24
		p.addTime(s.take().value)
		if !s.skip(':') || !s.next.fixed(2) || !between(s.next.value, 0, 59) || (is24 && s.next.value > 0) {
			return bad
		}
		p.addTime(s.take().value)
		if s.skip(':') {
			if !s.next.fixed(2) || !between(s.next.value, 0, 59) || (is24 && s.next.value > 0) {
				return bad
			}
			p.addTime(s.take().value)
			if s.skip('.') {
				if s.next.kind != number || (is24 && s.next.value > 0) {
					return bad
				}
				p.addTime(milliseconds(s.take()))
			}
		}
		if s.next.z() {
			s.take()
			p.zone(0)
		} else if s.next.sign() {
			p.tzSign = 44 - s.take().value
			if s.next.fixed(4) {
				n := s.take().value
				p.tzHour = n / 100
				p.tzMinute = n % 100
				if !between(p.tzHour, 0, 23) || !between(p.tzMinute, 0, 59) {
					return bad
				}
			} else {
				if !s.next.fixed(2) || !between(s.next.value, 0, 23) {
					return bad
				}
				p.tzHour = s.take().value
				if !s.skip(':') || !s.next.fixed(2) || !between(s.next.value, 0, 59) {
					return bad
				}
				p.tzMinute = s.take().value
			}
		}
		if s.next.kind != end {
			return bad
		}
	}
	if p.tzHour == -1 && len(p.clock) == 0 {
		p.zone(0)
	}
	p.isISO = true
	return token{kind: end}
}

// Finite evaluates source Date.parse validity. Construction validates the source timezone.
func (p *Parser) Finite(value string) (bool, error) {
	if p == nil || p.localToUTC == nil {
		return false, fmt.Errorf("jsdate: unconfigured parser")
	}
	return p.FiniteDateParse(value), nil
}
