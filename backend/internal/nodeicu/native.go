//go:build humanengine_icu && cgo

package nodeicu

/*
#include <stdint.h>
#include <unicode/udata.h>
#include <unicode/uclean.h>
#include <unicode/uversion.h>
#if U_ICU_VERSION_MAJOR_NUM != 78 || U_ICU_VERSION_MINOR_NUM != 3
#error "HumanEngine requires the exact pinned Node ICU 78.3 headers"
#endif
extern const uint8_t icudt78_dat[];
extern const uint8_t he_icu_data_end[];
static const uint8_t *shared_data(void) { return icudt78_dat; }
static uintptr_t shared_length(void) { return (uintptr_t)he_icu_data_end-(uintptr_t)icudt78_dat; }
static int shared_initialize(void) {
 UErrorCode status=U_ZERO_ERROR;
 udata_setFileAccess(UDATA_NO_FILES,&status);
 udata_setCommonData(icudt78_dat,&status);
 u_init(&status);
 return status;
}
static const char *shared_error(int code) { return u_errorName((UErrorCode)code); }
*/
import "C"

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

var initialization struct {
	once sync.Once
	err  error
	runs atomic.Uint32
}

// Ensure verifies the shared immutable data and initializes ICU once per Go
// process. Every ICU consumer must call it before any ICU API. No host data
// files, default locale/timezone, Node process, or teardown is involved.
func Ensure() error {
	initialization.once.Do(func() {
		initialization.runs.Add(1)
		if C.shared_length() != 33107952 {
			initialization.err = fmt.Errorf("nodeicu: bundled ICU data length mismatch")
			return
		}
		data := unsafe.Slice((*byte)(unsafe.Pointer(C.shared_data())), 33107952)
		if fmt.Sprintf("%x", sha256.Sum256(data)) != DataSHA256 {
			initialization.err = fmt.Errorf("nodeicu: bundled ICU data checksum mismatch")
			return
		}
		if code := C.shared_initialize(); code > 0 {
			initialization.err = fmt.Errorf("nodeicu: ICU initialization: %s", C.GoString(C.shared_error(code)))
		}
	})
	return initialization.err
}
