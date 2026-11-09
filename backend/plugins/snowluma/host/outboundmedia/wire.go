package outboundmedia

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type replacement struct {
	source Source
	token  string
	at     int
}

func encodeWire(value *jsjson.Object, sources []Source) ([]byte, error) {
	bad := errors.New("outbound_encoding_failed")
	params := value
	if value == nil && len(sources) > 0 {
		return nil, bad
	}
	// The source only dereferences params inside sources.map. With no
	// sources, even a truthy primitive params is an ordinary JSON member.
	if len(sources) > 0 && value != nil {
		if v, ok := value.Get("params"); ok && truthy(v) {
			var valid bool
			params, valid = v.(*jsjson.Object)
			if !valid {
				return nil, bad
			}
		}
	}
	replacements := make([]replacement, 0, len(sources))
	indices := map[int]bool{}
	for _, source := range sources {
		if indices[source.Index] || params == nil {
			return nil, bad
		}
		indices[source.Index] = true
		v, _ := params.Get("message")
		message, ok := v.([]any)
		if !ok || source.Index < 0 || source.Index >= len(message) {
			return nil, bad
		}
		part, ok := message[source.Index].(*jsjson.Object)
		if !ok || part == nil {
			return nil, bad
		}
		v, _ = part.Get("data")
		data, ok := v.(*jsjson.Object)
		if !ok || data == nil {
			return nil, bad
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		marker := "__humanengine_original_" + hex.EncodeToString(nonce[:]) + "__"
		if err := data.Set("file", marker); err != nil {
			return nil, err
		}
		token, err := jsjson.Stringify(marker)
		if err != nil {
			return nil, err
		}
		replacements = append(replacements, replacement{source: source, token: token})
	}
	template, err := jsjson.Stringify(value)
	if err != nil {
		return nil, err
	}
	length := int64(len(template))
	for i := range replacements {
		r := &replacements[i]
		r.at = strings.Index(template, r.token)
		if r.at < 0 || strings.Index(template[r.at+len(r.token):], r.token) >= 0 {
			return nil, bad
		}
		length += 4*((r.source.Bytes+2)/3) + 11 - int64(len(r.token))
	}
	if length > MaxWire {
		return nil, bad
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].at < replacements[j].at })
	wire := make([]byte, int(length))
	offset, cursor := 0, 0
	for _, r := range replacements {
		offset += copy(wire[offset:], template[cursor:r.at])
		offset += copy(wire[offset:], `"base64://`)
		n := base64.StdEncoding.EncodedLen(int(r.source.Bytes))
		if err := encodeFile(r.source, wire[offset:offset+n]); err != nil {
			return nil, err
		}
		offset += n
		offset += copy(wire[offset:], `"`)
		cursor = r.at + len(r.token)
	}
	offset += copy(wire[offset:], template[cursor:])
	if offset != len(wire) {
		return nil, bad
	}
	return wire, nil
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	default:
		return !jsjson.IsUndefined(v)
	}
}

// encodeFile allocates one fixed-size scratch buffer, never a source-size or
// base64-size intermediate. The entire wire remains private until final stat
// and SHA-256 validation have succeeded for every original source.
func encodeFile(source Source, dst []byte) error {
	bad := errors.New("outbound_media_changed")
	file, err := os.Open(source.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() != source.Bytes || before.Size() > MaxSource {
		return bad
	}
	hash := sha256.New()
	scratch := make([]byte, 48*1024)
	remaining := source.Bytes
	offset := 0
	for remaining > 0 {
		size := int64(len(scratch))
		if remaining < size {
			size = remaining
		}
		count, err := io.ReadFull(file, scratch[:size])
		if err != nil {
			return bad
		}
		chunk := scratch[:count]
		_, _ = hash.Write(chunk)
		n := base64.StdEncoding.EncodedLen(count)
		base64.StdEncoding.Encode(dst[offset:offset+n], chunk)
		offset += n
		remaining -= int64(count)
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !sameChangeTime(before, after) {
		return bad
	}
	if hex.EncodeToString(hash.Sum(nil)) != source.Hash {
		return bad
	}
	return nil
}

// os.FileInfo has no portable change-time accessor. Unix Stat_t uses Ctim or
// Ctimespec. Compare those fields without comparing atime, which our read can
// itself change. Windows ChangeTime is checked when exposed by its stat type.
func sameChangeTime(a, b os.FileInfo) bool {
	av, bv := reflect.ValueOf(a.Sys()), reflect.ValueOf(b.Sys())
	if av.Kind() == reflect.Pointer {
		av = av.Elem()
	}
	if bv.Kind() == reflect.Pointer {
		bv = bv.Elem()
	}
	if av.Kind() != reflect.Struct || bv.Kind() != reflect.Struct {
		return true
	}
	for _, name := range []string{"Ctim", "Ctimespec", "ChangeTime"} {
		x, y := av.FieldByName(name), bv.FieldByName(name)
		if x.IsValid() && y.IsValid() {
			return reflect.DeepEqual(x.Interface(), y.Interface())
		}
	}
	return true
}
