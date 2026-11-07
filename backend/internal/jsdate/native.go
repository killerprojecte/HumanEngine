//go:build humanengine_icu && cgo

package jsdate

/*
#include <unicode/ucal.h>
#include <stdlib.h>
#if U_ICU_VERSION_MAJOR_NUM != 78 || U_ICU_VERSION_MINOR_NUM != 3
#error "jsdate requires pinned Node ICU 78.3 headers"
#endif
// V8 intl-objects.cc LocalTimeOffset returns zero when GetOffsets fails.
// Subtracting that zero offset returns ms unchanged.
static double date_utc(const UChar *zone,int32_t length,double ms) {
 UErrorCode e=U_ZERO_ERROR;
 UCalendar *c=ucal_open(zone,length,"en_US_POSIX",UCAL_GREGORIAN,&e);
 if(U_FAILURE(e)) { ucal_close(c); return ms; }
 ucal_setMillis(c,ms,&e);
 int32_t raw=0,dst=0;
 ucal_getTimeZoneOffsetFromLocal(c,UCAL_TZ_LOCAL_FORMER,UCAL_TZ_LOCAL_FORMER,&raw,&dst,&e);
 ucal_close(c);
 return U_FAILURE(e)?ms:ms-raw-dst;
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
)

// New requires the source Node runtime's resolved IANA timezone identifier.
func New(sourceResolvedTimezone string) (*Parser, error) {
	if sourceResolvedTimezone == "UTC" {
		return &utcParser, nil
	}
	units, err := jsjson.UTF16(sourceResolvedTimezone)
	if err != nil || len(units) == 0 {
		return nil, fmt.Errorf("jsdate: explicit source-resolved timezone required")
	}
	if len(units) > 2147483647 {
		return nil, fmt.Errorf("jsdate: timezone exceeds ICU int32 ABI")
	}
	if err := nodeicu.Ensure(); err != nil {
		return nil, err
	}

	// Validate against ICU's system IDs instead of accepting its unknown-ID GMT fallback.
	var status C.UErrorCode
	var system C.UBool
	var result C.UChar
	size := C.ucal_getCanonicalTimeZoneID((*C.UChar)(unsafe.Pointer(&units[0])), C.int32_t(len(units)), &result, 1, &system, &status)
	// Capacity 1 deliberately preflights the identifier: overflow is expected
	// for real IDs. Only that specific failure (or ICU success/warnings) is allowed.
	if size <= 0 || system == 0 || status > 0 && status != C.U_BUFFER_OVERFLOW_ERROR {
		return nil, fmt.Errorf("jsdate: unknown source timezone %q", sourceResolvedTimezone)
	}
	return &Parser{localToUTC: func(ms int64) int64 {
		return int64(C.date_utc((*C.UChar)(unsafe.Pointer(&units[0])), C.int32_t(len(units)), C.double(ms)))
	}}, nil
}
