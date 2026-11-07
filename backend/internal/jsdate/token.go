package jsdate

const (
	invalid   = -6
	unknown   = -5
	space     = -4
	number    = -3
	symbol    = -2
	end       = -1
	keyword   = 0
	month     = 1
	zone      = 2
	separator = 3
	ampm      = 4
)

type token struct{ kind, value, length int }

func (t token) symbol(c int) bool { return t.kind == symbol && t.value == c }
func (t token) sign() bool        { return t.symbol('+') || t.symbol('-') }
func (t token) fixed(n int) bool  { return t.kind == number && t.length == n }
func (t token) z() bool           { return t.kind == zone && t.length == 1 && t.value == 0 }

type scanner struct {
	units []uint16
	pos   int
	next  token
}

func (s *scanner) ch() uint16 {
	if s.pos >= len(s.units) {
		return 0
	}
	return s.units[s.pos]
}
func (s *scanner) take() token { t := s.next; s.next = s.read(); return t }
func (s *scanner) skip(c int) bool {
	if !s.next.symbol(c) {
		return false
	}
	s.take()
	return true
}
func white(c uint16) bool {
	return c == 9 || c == 11 || c == 12 || c == 32 || c == 160 || c == 0xfeff || c == 0x1680 || between(int(c), 0x2000, 0x200a) || c == 0x202f || c == 0x205f || c == 0x3000
}
func (s *scanner) read() token {
	c := s.ch()
	start := s.pos
	if c == 0 {
		return token{kind: end}
	}
	if c >= '0' && c <= '9' {
		n, count := 0, 0
		for s.ch() == '0' {
			s.pos++
		}
		for s.ch() >= '0' && s.ch() <= '9' {
			if count < 9 {
				n = n*10 + int(s.ch()-'0')
			}
			count++
			s.pos++
		}
		return token{number, n, s.pos - start}
	}
	switch c {
	case ':', '-', '+', '.', ')':
		s.pos++
		return token{symbol, int(c), 1}
	}
	if c >= 'A' && !white(c) {
		var prefix [3]uint16
		l := 0
		for s.ch() >= 'A' && !white(s.ch()) {
			if l < 3 {
				v := s.ch()
				if v >= 'A' && v <= 'Z' {
					v += 32
				}
				prefix[l] = v
			}
			l++
			s.pos++
		}
		for _, k := range words {
			if prefix == k.prefix && (l <= 3 || k.kind == month) {
				return token{k.kind, k.value, l}
			}
		}
		return token{keyword, 0, l}
	}
	if white(c) || c == 10 || c == 13 || c == 0x2028 || c == 0x2029 {
		s.pos++
		return token{space, 0, 1}
	}
	if c == '(' {
		balance := 0
		for {
			c = s.ch()
			if c == ')' {
				balance--
			} else if c == '(' {
				balance++
			}
			s.pos++
			if balance <= 0 || s.ch() == 0 {
				break
			}
		}
		return token{kind: unknown}
	}
	s.pos++
	return token{kind: unknown}
}

type word struct {
	prefix      [3]uint16
	kind, value int
}

var words = []word{
	{[3]uint16{'j', 'a', 'n'}, month, 1}, {[3]uint16{'f', 'e', 'b'}, month, 2}, {[3]uint16{'m', 'a', 'r'}, month, 3}, {[3]uint16{'a', 'p', 'r'}, month, 4}, {[3]uint16{'m', 'a', 'y'}, month, 5}, {[3]uint16{'j', 'u', 'n'}, month, 6}, {[3]uint16{'j', 'u', 'l'}, month, 7}, {[3]uint16{'a', 'u', 'g'}, month, 8}, {[3]uint16{'s', 'e', 'p'}, month, 9}, {[3]uint16{'o', 'c', 't'}, month, 10}, {[3]uint16{'n', 'o', 'v'}, month, 11}, {[3]uint16{'d', 'e', 'c'}, month, 12},
	{[3]uint16{'a', 'm', 0}, ampm, 0}, {[3]uint16{'p', 'm', 0}, ampm, 12}, {[3]uint16{'u', 't', 0}, zone, 0}, {[3]uint16{'u', 't', 'c'}, zone, 0}, {[3]uint16{'z', 0, 0}, zone, 0}, {[3]uint16{'g', 'm', 't'}, zone, 0}, {[3]uint16{'c', 'd', 't'}, zone, -5}, {[3]uint16{'c', 's', 't'}, zone, -6}, {[3]uint16{'e', 'd', 't'}, zone, -4}, {[3]uint16{'e', 's', 't'}, zone, -5}, {[3]uint16{'m', 'd', 't'}, zone, -6}, {[3]uint16{'m', 's', 't'}, zone, -7}, {[3]uint16{'p', 'd', 't'}, zone, -7}, {[3]uint16{'p', 's', 't'}, zone, -8}, {[3]uint16{'t', 0, 0}, separator, 0},
}
