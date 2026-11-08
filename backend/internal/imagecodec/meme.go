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

type MemeMetadata struct{ Width, Height, Frames int }

func (c *Compressor) MemeMetadata(ctx context.Context, b []byte) (MemeMetadata, error) {
	if e := ctx.Err(); e != nil {
		return MemeMetadata{}, e
	}
	if c == nil || !c.ready {
		return MemeMetadata{}, errors.New("native_image_codec_not_initialized")
	}
	if len(b) == 0 {
		return MemeMetadata{}, errors.New("invalid_or_unsupported_image")
	}
	input := C.he_source_new(unsafe.Pointer(&b[0]), C.size_t(len(b)))
	if input == nil {
		return MemeMetadata{}, errors.New("native_image_allocation_failed")
	}
	defer C.he_source_free(input)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var m C.he_metadata
	var nativeError *C.char
	if C.he_meme_metadata(input, C.size_t(len(b)), &m, &nativeError) != 0 {
		if nativeError != nil {
			defer C.he_free(unsafe.Pointer(nativeError))
		}
		return MemeMetadata{}, errors.New("invalid_or_unsupported_image")
	}
	return MemeMetadata{int(m.width), int(m.page_height), int(m.pages)}, nil
}

// MemeFramePNG matches the analysis sample pipeline; it does not autorotate.
func (c *Compressor) MemeFramePNG(ctx context.Context, b []byte, page int) ([]byte, error) {
	return c.memeRender(ctx, b, page, false)
}

// MemeThumbnailWebP matches first-page autorotation and WebP quality 75.
func (c *Compressor) MemeThumbnailWebP(ctx context.Context, b []byte) ([]byte, error) {
	return c.memeRender(ctx, b, 0, true)
}
func (c *Compressor) memeRender(ctx context.Context, b []byte, page int, thumbnail bool) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if c == nil || !c.ready {
		return nil, errors.New("native_image_codec_not_initialized")
	}
	if len(b) == 0 {
		return nil, errors.New("invalid_or_unsupported_image")
	}
	if page < 0 || uint64(page) > 2147483647 {
		return nil, errors.New("invalid_image_page")
	}
	input := C.he_source_new(unsafe.Pointer(&b[0]), C.size_t(len(b)))
	if input == nil {
		return nil, errors.New("native_image_allocation_failed")
	}
	defer C.he_source_free(input)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var out unsafe.Pointer
	var n C.size_t
	var nativeError *C.char
	mode := 0
	if thumbnail {
		mode = 1
	}
	if C.he_meme_render(input, C.size_t(len(b)), C.int(page), C.int(mode), &out, &n, &nativeError) != 0 {
		if nativeError != nil {
			defer C.he_free(unsafe.Pointer(nativeError))
		}
		return nil, errors.New("invalid_or_unsupported_image")
	}
	defer C.he_free(out)
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return append([]byte(nil), unsafe.Slice((*byte)(out), int(n))...), nil
}
