//go:build linux

package outboundmedia

import (
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func availableMemory() uint64 {
	return (linuxMemoryProbe{
		slurp: slurpMemoryFile,
		sysinfo: func() (uint64, uint64, bool) {
			var info syscall.Sysinfo_t
			if syscall.Sysinfo(&info) != nil {
				return 0, 0, false
			}
			return uint64(info.Freeram) * uint64(info.Unit), uint64(info.Totalram) * uint64(info.Unit), true
		},
		pageSize: uint64(os.Getpagesize()), longMax: uint64(1)<<(strconv.IntSize-1) - 1,
	}).available()
}

// Match uv__slurp's one bounded read, including successful empty files and
// C-string termination. These bounds are the source probe's buffers, not new
// outbound-media limits.
func slurpMemoryFile(path string, size int) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	buf := make([]byte, size-1)
	n, err := file.Read(buf)
	if n == 0 && err != nil && err != io.EOF {
		return "", false
	}
	data := string(buf[:n])
	if at := strings.IndexByte(data, 0); at >= 0 {
		data = data[:at]
	}
	return data, true
}
