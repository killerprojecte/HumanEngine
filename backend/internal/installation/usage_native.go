//go:build humanengine_icu && cgo

package installation

/*
#include <unicode/ucal.h>
static int usage_parts(const UChar *zone,int32_t length,double ms,int *out) {
 UErrorCode e=U_ZERO_ERROR;
 UCalendar *c=ucal_open(zone,length,"en_CA",UCAL_GREGORIAN,&e);
 if(U_FAILURE(e)){ucal_close(c);return e;}
 ucal_setGregorianChange(c,-8640000000000000.0,&e);
 ucal_setMillis(c,ms,&e);
 out[0]=ucal_get(c,UCAL_YEAR,&e);out[1]=ucal_get(c,UCAL_MONTH,&e)+1;
 out[2]=ucal_get(c,UCAL_DATE,&e);out[3]=ucal_get(c,UCAL_HOUR_OF_DAY,&e);
 out[4]=ucal_get(c,UCAL_MINUTE,&e);out[5]=ucal_get(c,UCAL_SECOND,&e);
 out[6]=ucal_get(c,UCAL_ZONE_OFFSET,&e)+ucal_get(c,UCAL_DST_OFFSET,&e);
 ucal_close(c);return e;
}
*/
import "C"
import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"unicode/utf16"
	"unsafe"
)

func namedParts(ms int64, zone string) (calendarParts, error) {
	if e := nodeicu.Ensure(); e != nil {
		return calendarParts{}, e
	}
	units := utf16.Encode([]rune(zone))
	var out [7]C.int
	status := C.usage_parts((*C.UChar)(unsafe.Pointer(&units[0])), C.int32_t(len(units)), C.double(ms), &out[0])
	if status > 0 {
		return calendarParts{}, fmt.Errorf("usage ICU conversion failed (%d)", status)
	}
	return calendarParts{int(out[0]), int(out[1]), int(out[2]), int(out[3]), int(out[4]), int(out[5]), int(out[6])}, nil
}
