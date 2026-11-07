//go:build humanengine_icu && cgo

package topiccollation

/*
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
)

// Collator owns an immutable ICU collator. Compare and Close can be used
// concurrently. Close releases its native resources and is idempotent.
type Collator struct {
	mu     sync.Mutex
	native *C.UCollator
	info   Info
}

// New requires the resolved default locale captured from the source deployment.
// It deliberately never consults LANG or starts a Node process. ICU's native
// locale defaults are retained, including Thai punctuation and Danish case order.
func New(sourceResolvedLocale string) (*Collator, error) {
	if sourceResolvedLocale == "" || strings.ContainsRune(sourceResolvedLocale, 0) {
		return nil, ErrLocale
	}
	if err := nodeicu.Ensure(); err != nil {
		return nil, err
	}

	tag := C.CString(sourceResolvedLocale)
	defer C.free(unsafe.Pointer(tag))
	var info C.HEICUInfo
	var code C.int
	p := C.he_icu_open(tag, &info, &code)
	if p == nil {
		return nil, fmt.Errorf("%w: %s", ErrLocale, C.GoString(C.he_icu_error(code)))
	}
	c := &Collator{native: p, info: Info{
		RequestedLocale: sourceResolvedLocale,
		ValidLocale:     C.GoString(info.valid_locale), ActualLocale: C.GoString(info.actual_locale),
		ICU: C.GoString(&info.icu[0]), Unicode: C.GoString(&info.unicode[0]),
		CLDR: C.GoString(&info.cldr[0]), Data: C.GoString(&info.data[0]),
		Collator: C.GoString(&info.collator[0]), UCA: C.GoString(&info.uca[0]),
		Normalization: int(info.normalization), Strength: int(info.strength),
		Numeric: int(info.numeric), CaseFirst: int(info.case_first),
		AlternateHandling: int(info.alternate), CaseLevel: int(info.case_level),
	}}
	if c.info.ICU != "78.3" || c.info.Unicode != "17.0" || c.info.CLDR != "48.0" ||
		info.normalization != C.UCOL_ON || info.strength != C.UCOL_TERTIARY {
		C.ucol_close(p)
		return nil, fmt.Errorf("topiccollation: native version/attribute mismatch: %+v", c.info)
	}
	runtime.SetFinalizer(c, func(c *Collator) { _ = c.Close() })
	return c, nil
}

func (c *Collator) Info() Info { return c.info }

// Compare consumes canonical WTF-8 JS identities. Explicit UTF-16 lengths
// preserve embedded NULs and distinct lone surrogates. ICU uses signed 32-bit
// lengths; exceeding that intrinsic ABI width is an error, never truncation.
func (c *Collator) Compare(a, b string) (int, error) {
	au, err := jsjson.UTF16(a)
	if err != nil {
		return 0, err
	}
	bu, err := jsjson.UTF16(b)
	if err != nil {
		return 0, err
	}
	if len(au) > math.MaxInt32 || len(bu) > math.MaxInt32 {
		return 0, fmt.Errorf("topiccollation: UTF-16 length exceeds ICU int32 ABI")
	}
	an, bn := len(au), len(bu)
	// Non-null pointers are required even for empty strings. These sentinel
	// units are outside the explicit lengths and are never compared.
	if an == 0 {
		au = []uint16{0}
	}
	if bn == 0 {
		bu = []uint16{0}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native == nil {
		return 0, ErrClosed
	}
	result := C.ucol_strcoll(c.native, (*C.UChar)(unsafe.Pointer(&au[0])), C.int32_t(an), (*C.UChar)(unsafe.Pointer(&bu[0])), C.int32_t(bn))
	runtime.KeepAlive(au)
	runtime.KeepAlive(bu)
	return int(result), nil
}

func (c *Collator) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native != nil {
		C.ucol_close(c.native)
		c.native = nil
	}
	return nil
}
