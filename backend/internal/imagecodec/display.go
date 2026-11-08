package imagecodec

/*
#include "bridge.h"
*/
import "C"
import (
	"context"
	"errors"
	"runtime"
	"unsafe"
)

// ValidateDisplayImage retains original animation bytes and decodes frame zero,
// with the existing chat-media 64MiB/40M aggregate pixels/200-page policies.
func (c *Compressor) ValidateDisplayImage(ctx context.Context, b []byte) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if len(b) == 0 || len(b) > 64*1024*1024 {
		return "", errors.New("image_size_limit")
	}
	if c == nil || !c.ready {
		return "", errors.New("native_image_codec_not_initialized")
	}
	input := C.he_source_new(unsafe.Pointer(&b[0]), C.size_t(len(b)))
	if input == nil {
		return "", errors.New("native_image_allocation_failed")
	}
	defer C.he_source_free(input)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var m C.he_metadata
	if C.he_display_validate(input, C.size_t(len(b)), &m) != 0 {
		return "", errors.New("invalid_or_unsupported_image")
	}
	return "image/" + C.GoString(&m.format[0]), nil
}
