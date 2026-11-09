//go:build web_read_candidate

package host

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errFraming = errors.New("network_request_failed")

// These are Node 24.22.1's existing parser budgets, not new admission caps.
const headerBudget = 16 * 1024
const extensionBudget = 16 * 1024

type headerField struct{ name, value string }

func tokenByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0
}
func fieldByte(b byte) bool { return b == '\t' || b >= 32 && b != 127 }
func expectByte(r *bufio.Reader, want byte) error {
	b, err := r.ReadByte()
	if err != nil {
		return err
	}
	if b != want {
		return errFraming
	}
	return nil
}
func addHeader(size *int) error {
	*size++
	if *size >= headerBudget {
		return errFraming
	}
	return nil
}

// Parse only the response envelope incrementally before giving it to Go's
// standard response parser. Node rejects bare LF, obs-fold, duplicate lengths,
// and TE+CL that Go otherwise accepts. Discard leading OWS without allocating
// it or counting it against the source's header-name/value budget.
func readStatus(r *bufio.Reader) (string, int, error) {
	var line strings.Builder
	for i := 0; i < 12; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return "", 0, err
		}
		valid := false
		switch {
		case i < 5:
			valid = b == "HTTP/"[i]
		case i == 5 || i == 7 || i >= 9:
			valid = b >= '0' && b <= '9'
		case i == 6:
			valid = b == '.'
		case i == 8:
			valid = b == ' '
		}
		if !valid {
			return "", 0, errFraming
		}
		line.WriteByte(b)
	}
	b, err := r.ReadByte()
	if err != nil {
		return "", 0, err
	}
	if b == '\r' {
		if err = expectByte(r, '\n'); err != nil {
			return "", 0, err
		}
		return line.String() + "\r\n", 0, nil
	}
	if b != ' ' {
		return "", 0, errFraming
	}
	line.WriteByte(b)
	size := 0
	for {
		b, err = r.ReadByte()
		if err != nil {
			return "", 0, err
		}
		if b == '\r' {
			if err = expectByte(r, '\n'); err != nil {
				return "", 0, err
			}
			return line.String() + "\r\n", size, nil
		}
		if !fieldByte(b) {
			return "", 0, errFraming
		}
		if err = addHeader(&size); err != nil {
			return "", 0, err
		}
		line.WriteByte(b)
	}
}
func readFields(r *bufio.Reader, size int) ([]headerField, error) {
	var fields []headerField
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\r' {
			if err = expectByte(r, '\n'); err != nil {
				return nil, err
			}
			return fields, nil
		}
		var name, value strings.Builder
		for {
			if !tokenByte(b) {
				return nil, errFraming
			}
			if err = addHeader(&size); err != nil {
				return nil, err
			}
			name.WriteByte(b)
			b, err = r.ReadByte()
			if err != nil {
				return nil, err
			}
			if b == ':' {
				break
			}
		}
		for {
			b, err = r.ReadByte()
			if err != nil {
				return nil, err
			}
			if b != ' ' && b != '\t' {
				break
			}
		}
		for {
			if b == '\r' {
				if err = expectByte(r, '\n'); err != nil {
					return nil, err
				}
				break
			}
			if !fieldByte(b) {
				return nil, errFraming
			}
			if err = addHeader(&size); err != nil {
				return nil, err
			}
			value.WriteByte(b)
			b, err = r.ReadByte()
			if err != nil {
				return nil, err
			}
		}
		fields = append(fields, headerField{name.String(), value.String()})
	}
}
func readResponse(reader *bufio.Reader, method string) (*http.Response, *bufio.Reader, []headerField, error) {
	status, size, err := readStatus(reader)
	if err != nil {
		return nil, reader, nil, err
	}
	fields, err := readFields(reader, size)
	if err != nil {
		return nil, reader, nil, err
	}
	lengths := 0
	var transfer []string
	for _, f := range fields {
		switch strings.ToLower(f.name) {
		case "content-length":
			lengths++
		case "transfer-encoding":
			transfer = append(transfer, f.value)
		}
	}
	if lengths > 1 || lengths > 0 && len(transfer) > 0 {
		return nil, reader, nil, errFraming
	}
	chunked := false
	if len(transfer) > 0 {
		all := strings.Join(transfer, ",")
		if strings.TrimSpace(all) == "" {
			return nil, reader, nil, errFraming
		}
		codings := strings.Split(all, ",")
		// Node chooses framing from the last transfer coding. Earlier
		// chunked tokens neither force dechunking nor reject duplicates.
		chunked = strings.EqualFold(strings.TrimSpace(codings[len(codings)-1]), "chunked")
	}
	var header strings.Builder
	header.WriteString(status)
	for _, f := range fields {
		// Go only supports a sole chunked coding. Node also accepts transfer-gzip
		// and gzip,chunked without decompressing gzip. Framing is selected above;
		// the standard parser still validates all other response fields.
		if strings.EqualFold(f.name, "Transfer-Encoding") {
			continue
		}
		header.WriteString(f.name)
		header.WriteByte(':')
		header.WriteString(f.value)
		header.WriteString("\r\n")
	}
	header.WriteString("\r\n")
	combined := bufio.NewReader(io.MultiReader(strings.NewReader(header.String()), reader))
	response, err := http.ReadResponse(combined, &http.Request{Method: method})
	if err != nil {
		return nil, combined, nil, err
	}
	if response.StatusCode >= 100 && response.StatusCode < 200 {
		// Informational responses have no body. Continue directly on the
		// original reader instead of accumulating nested prefix readers.
		return response, reader, fields, nil
	}
	if chunked && method != http.MethodHead && response.StatusCode != 204 && response.StatusCode != 304 {
		// No body bytes have been consumed. Own chunk framing from that exact
		// reader rather than recovering after a standard decoder error. Go's
		// decoder adds 4096-byte line, overhead-ratio and 4KiB-trailer policies
		// that do not exist in the pinned source.
		response.Body = io.NopCloser(&chunkBody{reader: combined})
	}
	return response, combined, fields, nil
}

type chunkBody struct {
	reader         *bufio.Reader
	remaining      uint64
	needCRLF, done bool
}

func (b *chunkBody) Read(p []byte) (n int, err error) {
	// EOF is successful only after the zero chunk and complete trailers.
	// Delimiter/size/trailer reads can also hit EOF, not only payload reads.
	defer func() {
		if err == io.EOF && !b.done {
			err = io.ErrUnexpectedEOF
		}
	}()
	if len(p) == 0 {
		return 0, nil
	}
	if b.done {
		return 0, io.EOF
	}
	if b.remaining == 0 {
		if b.needCRLF {
			if err := expectByte(b.reader, '\r'); err != nil {
				return 0, err
			}
			if err := expectByte(b.reader, '\n'); err != nil {
				return 0, err
			}
			b.needCRLF = false
		}
		n, err := readChunkSize(b.reader)
		if err != nil {
			return 0, err
		}
		b.remaining = n
		if n == 0 {
			_, err = readFields(b.reader, 0)
			if err != nil {
				return 0, err
			}
			b.done = true
			return 0, io.EOF
		}
	}
	if uint64(len(p)) > b.remaining {
		p = p[:int(b.remaining)]
	}
	n, err = b.reader.Read(p)
	b.remaining -= uint64(n)
	if b.remaining == 0 {
		b.needCRLF = true
	}
	return n, err
}
func hexByte(b byte) (uint64, bool) {
	switch {
	case b >= '0' && b <= '9':
		return uint64(b - '0'), true
	case b >= 'a' && b <= 'f':
		return uint64(b - 'a' + 10), true
	case b >= 'A' && b <= 'F':
		return uint64(b - 'A' + 10), true
	}
	return 0, false
}
func readChunkSize(r *bufio.Reader) (uint64, error) {
	n := uint64(0)
	digits := false
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	for {
		v, ok := hexByte(b)
		if !ok {
			break
		}
		digits = true
		if n > (^uint64(0)-v)/16 {
			return 0, errFraming
		}
		n = n*16 + v
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
	}
	if !digits {
		return 0, errFraming
	}
	count := 0
	for {
		if b == '\r' {
			if err = expectByte(r, '\n'); err != nil {
				return 0, err
			}
			return n, nil
		}
		if b != ';' {
			return 0, errFraming
		}
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		if !tokenByte(b) {
			return 0, errFraming
		}
		for tokenByte(b) {
			count++
			if count > extensionBudget {
				return 0, errFraming
			}
			b, err = r.ReadByte()
			if err != nil {
				return 0, err
			}
		}
		if b != '=' {
			continue
		}
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == '"' {
			count++ // Source extension callback includes both quote bytes.
			if count > extensionBudget {
				return 0, errFraming
			}
			for {
				b, err = r.ReadByte()
				if err != nil {
					return 0, err
				}
				if b == '"' {
					count++
					if count > extensionBudget {
						return 0, errFraming
					}
					break
				}
				if !fieldByte(b) {
					return 0, errFraming
				}
				count++
				if count > extensionBudget {
					return 0, errFraming
				}
				if b == '\\' {
					b, err = r.ReadByte()
					if err != nil {
						return 0, err
					}
					if !fieldByte(b) {
						return 0, errFraming
					}
					count++
					if count > extensionBudget {
						return 0, errFraming
					}
				}
			}
			b, err = r.ReadByte()
			if err != nil {
				return 0, err
			}
		} else {
			if !tokenByte(b) {
				return 0, errFraming
			}
			for tokenByte(b) {
				count++
				if count > extensionBudget {
					return 0, errFraming
				}
				b, err = r.ReadByte()
				if err != nil {
					return 0, err
				}
			}
		}
	}
}
