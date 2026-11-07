//go:build humanengine_icu && cgo

package intltime

/*
#include <unicode/ucal.h>
static int intl_hour(const UChar *zone, int32_t length, double ms, int *hour) {
 UErrorCode status=U_ZERO_ERROR;
 UCalendar *calendar=ucal_open(zone,length,"en",UCAL_GREGORIAN,&status);
 if(U_FAILURE(status)) { ucal_close(calendar); return status; }
 ucal_setMillis(calendar,ms,&status);
 *hour=ucal_get(calendar,UCAL_HOUR_OF_DAY,&status);
 ucal_close(calendar);
 return status;
}
*/
import "C"

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"unicode/utf16"
	"unsafe"
)

func namedHour(ms int64, zone string) (int, error) {
	if err := nodeicu.Ensure(); err != nil {
		return 0, err
	}
	units := utf16.Encode([]rune(zone))
	var hour C.int
	status := C.intl_hour((*C.UChar)(unsafe.Pointer(&units[0])), C.int32_t(len(units)), C.double(ms), &hour)
	if status > 0 {
		return 0, fmt.Errorf("intltime: ICU hour conversion failed (%d)", status)
	}
	return int(hour), nil
}
