//go:build humanengine_icu && cgo

package memorylexical

/*
#include <stdlib.h>
#include <unicode/ubrk.h>
#include <unicode/uchar.h>
#include <unicode/uscript.h>
static UBreakIterator* he_words(const UChar *s,int n,UErrorCode *e){ return ubrk_open(UBRK_WORD,"zh",s,n,e); }
static int he_token(UChar32 c){int t=u_charType(c);return (t>=U_UPPERCASE_LETTER && t<=U_OTHER_LETTER) || t==U_DECIMAL_DIGIT_NUMBER || t==U_LETTER_NUMBER || t==U_OTHER_NUMBER || c=='_' || c==':' || c=='.' || c=='+' || c=='-';}
static int he_alpha(UChar32 c){int t=u_charType(c);return (t>=U_UPPERCASE_LETTER && t<=U_OTHER_LETTER) || t==U_DECIMAL_DIGIT_NUMBER || t==U_LETTER_NUMBER || t==U_OTHER_NUMBER || c=='_';}
static int he_han(UChar32 c){UErrorCode e=U_ZERO_ERROR;return uscript_getScript(c,&e)==USCRIPT_HAN;}
*/
import "C"
import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"math"
	"runtime"
	"unsafe"
)

func Words(s string) ([]string, error)         { return words(s, false) }
func WordLikeWords(s string) ([]string, error) { return words(s, true) }
func words(s string, wordLike bool) ([]string, error) {
	if e := nodeicu.Ensure(); e != nil {
		return nil, e
	}
	in, e := jsjson.UTF16(s)
	if e != nil {
		return nil, e
	}
	out := []string{}
	if len(in) == 0 {
		return out, nil
	}
	if len(in) > math.MaxInt32 {
		return nil, errors.New("memory_lexical_icu_text_width")
	}
	var pinned runtime.Pinner
	pinned.Pin(&in[0])
	defer pinned.Unpin()
	var code C.UErrorCode
	b := C.he_words((*C.UChar)(unsafe.Pointer(&in[0])), C.int(len(in)), &code)
	if code > 0 || b == nil {
		return nil, errors.New("memory_lexical_segment_failed")
	}
	defer C.ubrk_close(b)
	start := C.ubrk_first(b)
	for end := C.ubrk_next(b); end != C.UBRK_DONE; end = C.ubrk_next(b) {
		if !wordLike || C.ubrk_getRuleStatus(b) >= C.UBRK_WORD_NUMBER {
			out = append(out, jsjson.FromUTF16(in[int(start):int(end)]))
		}
		start = end
	}
	return out, nil
}
func point(in []uint16, i int) (rune, int) {
	r := rune(in[i])
	if r >= 0xd800 && r <= 0xdbff && i+1 < len(in) && in[i+1] >= 0xdc00 && in[i+1] <= 0xdfff {
		return 0x10000 + (r-0xd800)*0x400 + rune(in[i+1]) - 0xdc00, 2
	}
	return r, 1
}
func TokenRuns(s string) ([]string, error) { return runs(s, true) }
func tokenRuns(s string) ([]string, error) { return runs(s, false) }
func runs(s string, alpha bool) ([]string, error) {
	if e := nodeicu.Ensure(); e != nil {
		return nil, e
	}
	units, e := jsjson.UTF16(s)
	if e != nil {
		return nil, e
	}
	out := []string{}
	start := -1
	for i := 0; i < len(units); {
		r, n := point(units, i)
		if (!alpha && C.he_token(C.UChar32(r)) != 0) || (alpha && C.he_alpha(C.UChar32(r)) != 0) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			out = append(out, jsjson.FromUTF16(units[start:i]))
			start = -1
		}
		i += n
	}
	if start >= 0 {
		out = append(out, jsjson.FromUTF16(units[start:]))
	}
	return out, nil
}
func hanClass(s string) (some, all bool, count int) {
	units, _ := jsjson.UTF16(s)
	all = len(units) > 0
	for i := 0; i < len(units); {
		r, n := point(units, i)
		h := C.he_han(C.UChar32(r)) != 0
		some = some || h
		all = all && h
		count++
		i += n
	}
	return
}
