//go:build humanengine_icu && cgo

package topiccollation

/*
#cgo CXXFLAGS: -std=c++17
#include <stdbool.h>
#include <stdlib.h>
#include "native.h"

typedef struct HERequestedLocale HERequestedLocale;
HERequestedLocale* he_requested_open(const char*, bool*);
void he_requested_close(HERequestedLocale*);
bool he_requested_special_case(const HERequestedLocale*);
int32_t he_requested_lower(HERequestedLocale*, UChar*, int32_t, const UChar*, int32_t, UErrorCode*);
int32_t he_requested_sort_key(HERequestedLocale*, const UChar*, int32_t, uint8_t*, int32_t);
*/
import "C"

import (
	"errors"
	"math"
	"strings"
	"unsafe"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
)

// RequestedLocale owns one explicit admin request's locale configuration. It
// is distinct from New's source-resolved runtime default and is not shared.
type RequestedLocale struct {
	native  *C.HERequestedLocale
	special bool
}

func NewRequested(tag string) (*RequestedLocale, error) {
	if strings.ContainsRune(tag, 0) {
		return nil, ErrLocale
	}
	if err := nodeicu.Ensure(); err != nil {
		return nil, err
	}
	cTag := C.CString(tag)
	defer C.free(unsafe.Pointer(cTag))
	var invalid C.bool
	native := C.he_requested_open(cTag, &invalid)
	if native == nil {
		if invalid {
			return nil, ErrLocale
		}
		return nil, errors.New("topiccollation: requested native locale could not be opened")
	}
	return &RequestedLocale{native, bool(C.he_requested_special_case(native))}, nil
}

func (r *RequestedLocale) Close() { C.he_requested_close(r.native) }

func (r *RequestedLocale) Lower(value string) (string, error) {
	if !r.special || value == "" {
		return jsstring.Lower(value)
	}
	u, err := jsjson.UTF16(value)
	if err != nil {
		return "", err
	}
	if len(u) > math.MaxInt32 {
		return "", ErrLocale
	}
	input := (*C.UChar)(unsafe.Pointer(unsafe.SliceData(u)))
	out := make([]uint16, len(u))
	status := C.UErrorCode(0)
	n := C.he_requested_lower(r.native, (*C.UChar)(unsafe.Pointer(unsafe.SliceData(out))), C.int32_t(len(out)), input, C.int32_t(len(u)), &status)
	if status == C.U_BUFFER_OVERFLOW_ERROR {
		out = make([]uint16, n)
		status = 0
		n = C.he_requested_lower(r.native, (*C.UChar)(unsafe.Pointer(unsafe.SliceData(out))), n, input, C.int32_t(len(u)), &status)
	}
	if status > 0 {
		return "", errors.New("topiccollation: requested locale case conversion failed")
	}
	return jsjson.FromUTF16(out[:n]), nil
}

func (r *RequestedLocale) SortKey(value string) ([]byte, error) {
	u, err := jsjson.UTF16(value)
	if err != nil {
		return nil, err
	}
	if len(u) > math.MaxInt32 {
		return nil, ErrLocale
	}
	input := (*C.UChar)(unsafe.Pointer(unsafe.SliceData(u)))
	n := C.he_requested_sort_key(r.native, input, C.int32_t(len(u)), nil, 0)
	out := make([]byte, n)
	C.he_requested_sort_key(r.native, input, C.int32_t(len(u)), (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(out))), n)
	return out, nil
}
