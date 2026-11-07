#ifndef HUMANENGINE_TOPICCOLLATION_H
#define HUMANENGINE_TOPICCOLLATION_H
#include <stdint.h>
#include <unicode/ucol.h>
#include <unicode/uversion.h>

typedef struct {
    char icu[U_MAX_VERSION_STRING_LENGTH];
    char unicode[U_MAX_VERSION_STRING_LENGTH];
    char cldr[U_MAX_VERSION_STRING_LENGTH];
    char data[U_MAX_VERSION_STRING_LENGTH];
    char collator[U_MAX_VERSION_STRING_LENGTH];
    char uca[U_MAX_VERSION_STRING_LENGTH];
    const char *valid_locale;
    const char *actual_locale;
    int normalization, strength, numeric, case_first, alternate, case_level;
} HEICUInfo;

const uint8_t *he_icu_data(void);
uintptr_t he_icu_data_length(void);
int he_icu_initialize(void);
UCollator *he_icu_open(const char *tag, HEICUInfo *info, int *error);
const char *he_icu_error(int error);
#endif
