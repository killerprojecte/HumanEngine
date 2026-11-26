package outboundmedia

import (
	"math"
	"strconv"
	"strings"
)

// linuxMemoryProbe follows Node v24.22.1's libuv 1.52.1 linux.c memory
// functions. In particular, a valid cgroup budget is NOT minimized against
// MemAvailable and ancestors are NOT traversed. Those would add admission
// policy that the source does not have. IO and sysinfo are injectable for the
// official-source fixture oracle; production supplies real Linux probes.
type linuxMemoryProbe struct {
	slurp    func(path string, size int) (string, bool)
	sysinfo  func() (free, total uint64, ok bool)
	pageSize uint64
	longMax  uint64
}

func (p linuxMemoryProbe) available() uint64 {
	group, ok := p.slurp("/proc/self/cgroup", 1024)
	if !ok {
		return 0
	}
	v2 := strings.HasPrefix(group, "0::/")
	high, maximum := p.limits(group, v2)
	if high == 0 || maximum == 0 {
		return p.free()
	}
	constrained := min(high, maximum)
	if constrained > p.total() {
		return p.free()
	}
	var current uint64
	if v2 {
		current = p.readUint(cgroup2Path(group, "memory.current"))
	} else {
		if path, ok := cgroup1Path(group, "memory.usage_in_bytes"); ok {
			current = p.readUint(path)
		}
		if current == 0 {
			current = p.readUint("/sys/fs/cgroup/memory/memory.usage_in_bytes")
		}
	}
	if constrained < current {
		return 0
	}
	return constrained - current
}
func (p linuxMemoryProbe) limits(group string, v2 bool) (high, maximum uint64) {
	if v2 {
		maximum = p.readUint(cgroup2Path(group, "memory.max"))
		high = p.readUint(cgroup2Path(group, "memory.high"))
		return
	}
	if path, ok := cgroup1Path(group, "memory.soft_limit_in_bytes"); ok {
		high = p.readUint(path)
		path, _ = cgroup1Path(group, "memory.limit_in_bytes")
		maximum = p.readUint(path)
	}
	if high == 0 || maximum == 0 {
		high = p.readUint("/sys/fs/cgroup/memory/memory.soft_limit_in_bytes")
		maximum = p.readUint("/sys/fs/cgroup/memory/memory.limit_in_bytes")
	}
	sentinel := p.longMax & ^(p.pageSize - 1)
	if high == sentinel {
		high = math.MaxUint64
	}
	if maximum == sentinel {
		maximum = math.MaxUint64
	}
	return
}
func (p linuxMemoryProbe) meminfo(name string) uint64 {
	data, ok := p.slurp("/proc/meminfo", 4096)
	if !ok {
		return 0
	}
	at := strings.Index(data, name)
	if at < 0 {
		return 0
	}
	n, _ := scanUint(data[at+len(name):])
	return n * 1024
}
func (p linuxMemoryProbe) free() uint64 {
	if n := p.meminfo("MemAvailable:"); n != 0 {
		return n
	}
	free, _, ok := p.sysinfo()
	if !ok {
		return 0
	}
	return free
}
func (p linuxMemoryProbe) total() uint64 {
	if n := p.meminfo("MemTotal:"); n != 0 {
		return n
	}
	_, total, ok := p.sysinfo()
	if !ok {
		return 0
	}
	return total
}
func (p linuxMemoryProbe) readUint(path string) uint64 {
	data, ok := p.slurp(path, 32)
	if !ok {
		return 0
	}
	if n, ok := scanUint(data); ok {
		return n
	}
	if data == "max\n" {
		return math.MaxUint64
	}
	return 0
}

// scanUint models sscanf(..., "%" PRIu64): leading C whitespace, optional
// sign, a decimal prefix, unsigned wrap for a negative value, and saturation
// on overflow. Procfs does not normally need the latter cases; keeping them
// makes malformed/truncated-file behavior testable against the actual C code.
func scanUint(s string) (uint64, bool) {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	negative := false
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		negative = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	value, err := strconv.ParseUint(s[:n], 10, 64)
	if err != nil {
		return math.MaxUint64, true
	}
	if negative {
		return -value, true
	}
	return value, true
}
func cgroup1Path(group, suffix string) (string, bool) {
	for _, line := range strings.Split(group, "\n") {
		at := strings.IndexByte(line, ':')
		if at >= 0 && strings.HasPrefix(line[at:], ":memory:") {
			rest := line[at+len(":memory:"):]
			// Kernel controller paths begin with /. The C implementation skips it.
			if len(rest) > 0 {
				rest = rest[1:]
			}
			return limitFilename("/sys/fs/cgroup/memory/" + rest + "/" + suffix), true
		}
	}
	return "", false
}
func cgroup2Path(group, suffix string) string {
	path := strings.SplitN(group[len("0::/"):], "\n", 2)[0]
	return limitFilename("/sys/fs/cgroup/" + path + "/" + suffix)
}
func limitFilename(path string) string {
	if len(path) > 4096 {
		return path[:4096]
	}
	return path
}
