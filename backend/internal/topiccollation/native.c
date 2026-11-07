//go:build humanengine_icu && cgo

#include "native.h"
#include <string.h>
#include <stdlib.h>
#include <limits.h>
#include <unicode/uchar.h>
#include <unicode/udata.h>
#include <unicode/uloc.h>
#include <unicode/ulocdata.h>
#include <unicode/icudataver.h>
#include <unicode/uclean.h>

#if U_ICU_VERSION_MAJOR_NUM != 78 || U_ICU_VERSION_MINOR_NUM != 3
#error "HumanEngine requires the exact pinned Node ICU 78.3 headers"
#endif

extern const uint8_t icudt78_dat[];
extern const uint8_t he_icu_data_end[];
const uint8_t *he_icu_data(void) { return icudt78_dat; }
uintptr_t he_icu_data_length(void) { return (uintptr_t)he_icu_data_end - (uintptr_t)icudt78_dat; }
const char *he_icu_error(int error) { return u_errorName((UErrorCode)error); }

int he_icu_initialize(void) {
    UErrorCode status = U_ZERO_ERROR;
    // The entire data package is linked into the binary and hashed by Go before
    // this call. ICU_DATA and host resource files must not replace that data.
    udata_setFileAccess(UDATA_NO_FILES, &status);
    udata_setCommonData(icudt78_dat, &status);
    u_init(&status);
    return status;
}

UCollator *he_icu_open(const char *tag, HEICUInfo *info, int *error) {
    UErrorCode status = U_ZERO_ERROR;
    // ICU can return a successful non-termination warning when output exactly
    // fills a caller's buffer. Preflight and reserve the terminator explicitly;
    // ULOC_FULLNAME_CAPACITY is not a limit on valid locale identifiers.
    size_t tag_length = strlen(tag);
    if (tag_length > INT32_MAX) { *error = U_ILLEGAL_ARGUMENT_ERROR; return NULL; }
    int32_t parsed = 0;
    int32_t length = uloc_forLanguageTag(tag, NULL, 0, &parsed, &status);
    if ((U_FAILURE(status) && status != U_BUFFER_OVERFLOW_ERROR) ||
        parsed != (int32_t)tag_length || length <= 0 || length == INT32_MAX) {
        *error = U_ILLEGAL_ARGUMENT_ERROR;
        return NULL;
    }
    char *locale = (char *)malloc((size_t)length + 1);
    if (!locale) { *error = U_MEMORY_ALLOCATION_ERROR; return NULL; }
    status = U_ZERO_ERROR;
    uloc_forLanguageTag(tag, locale, length + 1, &parsed, &status);
    if (U_FAILURE(status) || status == U_STRING_NOT_TERMINATED_WARNING ||
        parsed != (int32_t)tag_length || !locale[0]) {
        free(locale);
        *error = U_ILLEGAL_ARGUMENT_ERROR;
        return NULL;
    }
    // This API takes a source-resolved locale, not an arbitrary requested locale.
    // Reject unknown languages instead of silently inheriting the host default.
    char language[ULOC_LANG_CAPACITY];
    uloc_getLanguage(locale, language, sizeof(language), &status);
    int available = 0;
    for (int32_t i = 0; i < ucol_countAvailable(); i++) {
        char supported[ULOC_LANG_CAPACITY];
        uloc_getLanguage(ucol_getAvailable(i), supported, sizeof(supported), &status);
        if (!strcmp(language, supported)) { available = 1; break; }
    }
    if (!available || U_FAILURE(status)) { free(locale); *error = U_ILLEGAL_ARGUMENT_ERROR; return NULL; }
    UCollator *c = ucol_open(locale, &status);
    free(locale);
    if (U_FAILURE(status) || !c) { *error = status; return NULL; }
    // Match js-collator.cc default sort constructor. In particular do NOT force
    // punctuation/numeric/case-first: defaults can vary by locale (e.g. Thai).
    status = U_ZERO_ERROR;
    ucol_setAttribute(c, UCOL_NORMALIZATION_MODE, UCOL_ON, &status);
    ucol_setStrength(c, UCOL_TERTIARY);
    UVersionInfo version;
    u_getVersion(version); u_versionToString(version, info->icu);
    u_getUnicodeVersion(version); u_versionToString(version, info->unicode);
    ulocdata_getCLDRVersion(version, &status); u_versionToString(version, info->cldr);
    u_getDataVersion(version, &status); u_versionToString(version, info->data);
    ucol_getVersion(c, version); u_versionToString(version, info->collator);
    ucol_getUCAVersion(c, version); u_versionToString(version, info->uca);
    info->valid_locale = ucol_getLocaleByType(c, ULOC_VALID_LOCALE, &status);
    info->actual_locale = ucol_getLocaleByType(c, ULOC_ACTUAL_LOCALE, &status);
    info->normalization = ucol_getAttribute(c, UCOL_NORMALIZATION_MODE, &status);
    info->strength = ucol_getStrength(c);
    info->numeric = ucol_getAttribute(c, UCOL_NUMERIC_COLLATION, &status);
    info->case_first = ucol_getAttribute(c, UCOL_CASE_FIRST, &status);
    info->alternate = ucol_getAttribute(c, UCOL_ALTERNATE_HANDLING, &status);
    info->case_level = ucol_getAttribute(c, UCOL_CASE_LEVEL, &status);
    if (U_FAILURE(status)) { ucol_close(c); *error = status; return NULL; }
    *error = 0;
    return c;
}
