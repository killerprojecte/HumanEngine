// Package imagecodec implements the native pixel boundary using pinned libvips.
package imagecodec

/*
#cgo linux LDFLAGS: -ldl -lm
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

const LibrarySHA256 = "4aa73553408c3964071728f4a231a8e86918e39bc49f1fbae75355f7ea933ea9"

var initialization struct {
	sync.Mutex
	attempted bool
	err       error
}

type Compressor struct{ ready bool }

var _ providertransport.ImageCompressor = (*Compressor)(nil)

// New verifies and opens the exact deployable official binary. Supply an absolute
// distribution path, not a build-tree or node_modules dependency.
func New(path string) (*Compressor, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	fd, sealErr := C.he_seal_library(C.int(f.Fd()))
	f.Close()
	if fd < 0 {
		return nil, fmt.Errorf("native_image_library_snapshot: %w", sealErr)
	}
	sealed := os.NewFile(uintptr(fd), "sealed-libvips")
	defer sealed.Close()
	h := sha256.New()
	_, e = io.Copy(h, sealed)
	if e != nil {
		return nil, e
	}
	if hex.EncodeToString(h.Sum(nil)) != LibrarySHA256 {
		return nil, errors.New("native_image_library_hash_mismatch")
	}
	initialization.Lock()
	defer initialization.Unlock()
	if !initialization.attempted {
		initialization.attempted = true
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		p := C.CString(fmt.Sprintf("/proc/self/fd/%d", sealed.Fd()))
		defer C.free(unsafe.Pointer(p))
		if C.he_init(p) != 0 {
			initialization.err = fmt.Errorf("native_image_library: %s", C.GoString(C.he_error()))
		}
	}
	if initialization.err != nil {
		return nil, initialization.err
	}
	return &Compressor{ready: true}, nil
}
func value(p *int, d int) int {
	if p != nil {
		return *p
	}
	return d
}
func dataURL(b []byte, f string) string {
	return "data:image/" + f + ";base64," + base64.StdEncoding.EncodeToString(b)
}
func (c *Compressor) Compress(ctx context.Context, bytes []byte, limits providertransport.ImageUploadLimits, firstFrame bool) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if c == nil || !c.ready {
		return "", errors.New("native_image_codec_not_initialized")
	}
	if len(bytes) == 0 || len(bytes) > 64*1024*1024 {
		return "", errors.New("media_size_limit")
	}
	maxBytes, mw, mh := value(limits.MaxImageBytes, 10*1024*1024), value(limits.MaxWidth, 1536), value(limits.MaxHeight, 1536)
	for _, v := range []int{maxBytes, mw, mh} {
		if v < 1 || uint64(v) > 9007199254740991 {
			return "", errors.New("invalid_image_upload_limits")
		}
	}
	// One reference-counted C allocation survives all lazy/cache graphs without
	// copying the source for each quality trial or disabling cache reuse.
	input := C.he_source_new(unsafe.Pointer(&bytes[0]), C.size_t(len(bytes)))
	if input == nil {
		return "", errors.New("native_image_allocation_failed")
	}
	defer C.he_source_free(input)
	var m C.he_metadata
	runtime.LockOSThread()
	rc := C.he_inspect(input, C.size_t(len(bytes)), &m)
	runtime.UnlockOSThread()
	if rc != 0 {
		return "", errors.New("invalid_or_unsupported_image")
	}
	w, h := int(m.width), int(m.page_height)
	if m.orientation >= 5 {
		w, h = h, w
	}
	animated := m.pages > 1
	if (!animated || !firstFrame) && len(bytes) <= maxBytes && w <= mw && h <= mh {
		return dataURL(bytes, C.GoString(&m.format[0])), nil
	}
	if animated && !firstFrame {
		return "", errors.New("animated_image_compression_requires_first_frame")
	}
	scale := math.Min(1, math.Min(float64(mw)/float64(w), float64(mh)/float64(h)))
	render := func(tw, th, q int) ([]byte, error) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var output unsafe.Pointer
		var n C.size_t
		var nativeError *C.char
		if C.he_render(input, C.size_t(len(bytes)), C.int(tw), C.int(th), C.int(q), &output, &n, &nativeError) != 0 {
			defer C.he_free(unsafe.Pointer(nativeError))
			return nil, errors.New(strings.TrimRight(C.GoString(nativeError), " \n\r\f"))
		}
		defer C.he_free(output)
		// C.GoBytes takes an int32 length. A slice copy preserves the native size_t.
		return append([]byte(nil), unsafe.Slice((*byte)(output), int(n))...), nil
	}
	for {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		tw, th := max(1, int(math.Floor(float64(w)*scale))), max(1, int(math.Floor(float64(h)*scale)))
		png, e := render(tw, th, -1)
		if e != nil {
			return "", e
		}
		if len(png) <= maxBytes {
			return dataURL(png, "png"), nil
		}
		if m.alpha == 0 {
			lo, hi := 40, 92
			var best []byte
			for lo <= hi {
				if e := ctx.Err(); e != nil {
					return "", e
				}
				q := (lo + hi) / 2
				jpeg, e := render(tw, th, q)
				if e != nil {
					return "", e
				}
				if len(jpeg) <= maxBytes {
					best = jpeg
					lo = q + 1
				} else {
					hi = q - 1
				}
			}
			if best != nil {
				return dataURL(best, "jpeg"), nil
			}
		}
		if tw == 1 && th == 1 {
			break
		}
		scale *= 0.7
	}
	return "", errors.New("image_upload_limit_unachievable")
}

// Test hooks are package-private and only invoked after all test work is joined.
func trimCacheForTest()        { C.he_trim_cache_for_test() }
func liveInputsForTest() int64 { return int64(C.he_live_inputs()) }
