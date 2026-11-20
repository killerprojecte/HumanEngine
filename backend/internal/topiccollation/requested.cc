//go:build humanengine_icu && cgo

#include <algorithm>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <set>
#include <string>
#include <unicode/locid.h>
#include <unicode/localebuilder.h>
#include <unicode/strenum.h>
#include <unicode/ucol.h>
#include <unicode/udata.h>
#include <unicode/ures.h>
#include <unicode/ustring.h>

struct HERequestedLocale {
    UCollator* collator;
    std::string case_language;
};

namespace {
// ECMA-402's unicode_language_id prefix, followed by ICU validation of the
// complete tag. Reject grandfathered, extlang, and private-use-only prefixes.
bool alpha(const std::string& s, size_t min, size_t max) {
    return s.size() >= min && s.size() <= max &&
        std::all_of(s.begin(), s.end(), [](char c) { return c >= 'a' && c <= 'z'; });
}
bool digits(const std::string& s) {
    return std::all_of(s.begin(), s.end(), [](char c) { return c >= '0' && c <= '9'; });
}
bool alnum(const std::string& s) {
    return std::all_of(s.begin(), s.end(), [](char c) {
        return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9');
    });
}
bool valid_prefix(const std::string& s) {
    size_t i = 0;
    auto token = [&]() {
        auto end = s.find('-', i);
        auto part = s.substr(i, end == std::string::npos ? end : end - i);
        i = end == std::string::npos ? s.size() : end + 1;
        return part;
    };
    auto language = token();
    if (!alpha(language, 2, 3) && !alpha(language, 5, 8)) return false;
    if (i == s.size()) return true;
    auto part = token();
    if (alpha(part, 4, 4)) {
        if (i == s.size()) return true;
        part = token();
    }
    if (alpha(part, 2, 2) || (part.size() == 3 && digits(part))) {
        if (i == s.size()) return true;
        part = token();
    }
    for (;;) {
        if (!alnum(part) || part.empty()) return false;
        if (part.size() == 1) return true;
        if (part.size() > 8 || part.size() < 4 ||
            (part.size() == 4 && (part[0] < '0' || part[0] > '9'))) return false;
        if (i == s.size()) return true;
        part = token();
    }
}

bool has_collation_resource(const icu::Locale& locale) {
    UErrorCode status = U_ZERO_ERROR;
    UResourceBundle* bundle = ures_open(U_ICUDATA_NAME U_TREE_SEPARATOR_STRING "coll",
                                       locale.getName(), &status);
    ures_close(bundle);
    if (status == U_ZERO_ERROR) return true;
    const char* language = locale.getLanguage();
    const char* script = locale.getScript();
    const char* country = locale.getCountry();
    if (*country && *script)
        return has_collation_resource(icu::Locale(std::string(language).append("-").append(script).c_str()));
    if (*country || *script) return has_collation_resource(icu::Locale(language));
    return false;
}

const std::set<std::string>& available() {
    // Match the pinned Node Collator locale set, including scriptless aliases
    // and the nb -> no resource fallback. Built once, not once per request.
    static const auto result = [] {
        std::set<std::string> locales;
        for (int32_t i = 0; i < ucol_countAvailable(); ++i) {
            UErrorCode status = U_ZERO_ERROR;
            auto tag = icu::Locale(ucol_getAvailable(i)).toLanguageTag<std::string>(status);
            if (U_FAILURE(status)) continue;
            if (!has_collation_resource(icu::Locale(tag.c_str())) &&
                (tag != "nb" || !has_collation_resource(icu::Locale("no")))) continue;
            locales.insert(tag);
            auto canonical = icu::Locale::createCanonical(tag.c_str());
            if (*canonical.getScript()) {
                auto shortened = std::string(icu::Locale(canonical.getLanguage(), canonical.getCountry()).getName());
                std::replace(shortened.begin(), shortened.end(), '_', '-');
                locales.insert(shortened);
            }
        }
        return locales;
    }();
    return result;
}

std::string lookup(std::string tag) {
    const auto& locales = available();
    for (;;) {
        if (locales.find(tag) != locales.end()) return tag;
        auto end = tag.rfind('-');
        if (end == std::string::npos) return {};
        if (end >= 2 && tag[end - 2] == '-') end -= 2;
        tag.resize(end);
    }
}

bool valid_collation(const icu::Locale& locale, const std::string& value) {
    if (value == "standard" || value == "search") return false;
    const char* legacy = uloc_toLegacyType("collation", value.c_str());
    if (!legacy) return false;
    UErrorCode status = U_ZERO_ERROR;
    UEnumeration* choices = ucol_getKeywordValuesForLocale("collation", locale.getBaseName(), false, &status);
    bool found = false;
    if (U_SUCCESS(status)) {
        for (const char* item; (item = uenum_next(choices, nullptr, &status));)
            if (!std::strcmp(item, legacy)) { found = true; break; }
    }
    uenum_close(choices);
    return found;
}
} // namespace

extern "C" HERequestedLocale* he_requested_open(const char* input, bool* invalid) {
    *invalid = true;
    std::string tag(input);
    for (char& c : tag) {
        if (static_cast<unsigned char>(c) > 127) return nullptr;
        if (c >= 'A' && c <= 'Z') c += 'a' - 'A';
    }
    if (!valid_prefix(tag)) return nullptr;
    UErrorCode status = U_ZERO_ERROR;
    auto locale = icu::Locale::forLanguageTag(tag, status);
    locale = icu::LocaleBuilder().setLocale(locale).build(status);
    locale.canonicalize(status);
    if (U_FAILURE(status) || locale.isBogus()) return nullptr;
    tag = locale.toLanguageTag<std::string>(status);
    if (U_FAILURE(status)) return nullptr;

    // Only the Unicode extension is omitted from locale lookup, and it is
    // appended to the matched locale only when it precedes private use.
    std::string extension;
    auto unicode = tag.find("-u-");
    auto private_use = tag.find("-x-");
    if (unicode != std::string::npos && (private_use == std::string::npos || private_use > unicode)) {
        auto end = tag.size();
        for (auto i = unicode + 3; i + 2 < tag.size(); ++i) {
            if (tag[i] == '-' && tag[i + 2] == '-') { end = i; break; }
        }
        extension = tag.substr(unicode, end - unicode);
        tag.erase(unicode, end - unicode);
    }
    auto matched = lookup(tag);
    if (matched.empty()) return nullptr;
    *invalid = false;

    std::string language(locale.getLanguage());
    if (language != "tr" && language != "az" && language != "lt" && language != "el") language.clear();
    auto resolved = icu::Locale::forLanguageTag(matched + extension, status);
    icu::LocaleBuilder builder;
    builder.setLocale(resolved).clearExtensions();
    auto keyword = [&](const char* key) {
        UErrorCode own_status = U_ZERO_ERROR;
        return resolved.getUnicodeKeywordValue<std::string>(key, own_status);
    };
    auto co = keyword("co"), kf = keyword("kf"), kn = keyword("kn");
    if (!co.empty() && valid_collation(resolved, co)) builder.setUnicodeLocaleKeyword("co", co);
    if (kn == "true" || kn == "false") builder.setUnicodeLocaleKeyword("kn", kn);
    if (kf == "upper" || kf == "lower" || kf == "false") builder.setUnicodeLocaleKeyword("kf", kf);
    auto selected = builder.build(status);
    auto collator = ucol_open(selected.getName(), &status);
    if (U_FAILURE(status) || !collator) {
        status = U_ZERO_ERROR;
        collator = ucol_open(selected.getBaseName(), &status);
        if (U_FAILURE(status) || !collator) return nullptr;
    }
    status = U_ZERO_ERROR;
    if (kn == "true" || kn == "false")
        ucol_setAttribute(collator, UCOL_NUMERIC_COLLATION, kn == "true" ? UCOL_ON : UCOL_OFF, &status);
    if (kf == "upper" || kf == "lower" || kf == "false") {
        auto first = kf == "upper" ? UCOL_UPPER_FIRST : kf == "lower" ? UCOL_LOWER_FIRST : UCOL_OFF;
        ucol_setAttribute(collator, UCOL_CASE_FIRST, first, &status);
    }
    ucol_setAttribute(collator, UCOL_NORMALIZATION_MODE, UCOL_ON, &status);
    ucol_setStrength(collator, UCOL_TERTIARY);
    if (U_FAILURE(status)) { ucol_close(collator); return nullptr; }
    return new HERequestedLocale{collator, std::move(language)};
}

extern "C" void he_requested_close(HERequestedLocale* locale) {
    ucol_close(locale->collator);
    delete locale;
}
extern "C" bool he_requested_special_case(const HERequestedLocale* locale) {
    return !locale->case_language.empty();
}
extern "C" int32_t he_requested_lower(HERequestedLocale* locale, UChar* out, int32_t capacity,
                                      const UChar* input, int32_t length, UErrorCode* status) {
    return u_strToLower(out, capacity, input, length, locale->case_language.c_str(), status);
}
extern "C" int32_t he_requested_sort_key(HERequestedLocale* locale, const UChar* input, int32_t length,
                                         uint8_t* out, int32_t capacity) {
    return ucol_getSortKey(locale->collator, input, length, out, capacity);
}
