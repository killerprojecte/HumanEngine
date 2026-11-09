package observability

import (
	"embed"
	"math"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// These are byte-identical source default-locale catalogs. Sink output remains
// unchanged: this formatter owns only the source-compatible retained view.
//
//go:embed retained_catalog/*.json
var retainedCatalogFiles embed.FS
var retainedFlow = retainedCatalog("zh-CN")
var retainedPresentation = retainedCatalog("presentation-zh-CN")
var retainedErrors = retainedCatalog("errors-zh-CN")
var retainedHTTPCode = regexp.MustCompile(`^provider_http_[1-5]\d\d$`)
var retainedServerCode = regexp.MustCompile(`^provider_http_5\d\d$`)

func retainedCatalog(name string) *jsjson.Object {
	data, err := retainedCatalogFiles.ReadFile("retained_catalog/" + name + ".json")
	if err != nil {
		panic(err)
	}
	value, err := jsjson.ParseBytes(data)
	if err != nil {
		panic(err)
	}
	return value.(*jsjson.Object)
}
func retainedGet(o *jsjson.Object, key string) any {
	if o == nil {
		return jsjson.Undefined
	}
	value, ok := o.Get(key)
	if !ok {
		return jsjson.Undefined
	}
	return value
}
func retainedObject(v any) *jsjson.Object { o, _ := v.(*jsjson.Object); return o }
func retainedTruthy(v any) bool {
	if v == nil || jsjson.IsUndefined(v) {
		return false
	}
	switch x := v.(type) {
	case string:
		return x != ""
	case retainedNativeFunction:
		return true
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}
func retainedText(v any) string {
	if jsjson.IsUndefined(v) {
		return "undefined"
	}
	if v == nil {
		return "null"
	}
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) {
			return "NaN"
		}
		if math.IsInf(x, 1) {
			return "Infinity"
		}
		if math.IsInf(x, -1) {
			return "-Infinity"
		}
		text, _ := jsjson.Stringify(x)
		return text
	case string:
		return x
	case retainedNativeFunction:
		return "function " + string(x) + "() { [native code] }"
	case *jsjson.Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			if v != nil && !jsjson.IsUndefined(v) {
				parts[i] = retainedText(v)
			}
		}
		return strings.Join(parts, ",")
	default:
		s, _ := jsjson.Stringify(v)
		return s
	}
}
func retainedSame(a, b any) bool {
	if a == nil {
		return b == nil
	}
	if jsjson.IsUndefined(a) {
		return jsjson.IsUndefined(b)
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case *jsjson.Object:
		y, ok := b.(*jsjson.Object)
		return ok && x == y
	}
	return false
}
func retainedCoalesce(a, b any) any {
	if a == nil || jsjson.IsUndefined(a) {
		return b
	}
	return a
}

type retainedNativeFunction string

var retainedObjectPrototype = new(jsjson.Object)

// Source catalog objects have Object.prototype, unlike parsed log data. Preserve
// its inherited lookup results for unfamiliar but valid string labels.
func retainedCatalogLookup(object *jsjson.Object, key string) any {
	if object != nil {
		if value, ok := object.Get(key); ok {
			return value
		}
	}
	switch key {
	case "__proto__":
		return retainedObjectPrototype
	case "constructor":
		return retainedNativeFunction("Object")
	case "__defineGetter__", "__defineSetter__", "hasOwnProperty", "__lookupGetter__", "__lookupSetter__", "isPrototypeOf", "propertyIsEnumerable", "toString", "valueOf", "toLocaleString":
		return retainedNativeFunction(key)
	}
	return jsjson.Undefined
}
func retainedLabel(catalog *jsjson.Object, section string, value any) string {
	table := retainedObject(retainedGet(catalog, section))
	return retainedText(retainedCoalesce(retainedCatalogLookup(table, retainedText(value)), value))
}
func retainedWord(key string) string { return retainedLabel(retainedPresentation, "words", key) }
func retainedLocalized(code any) (summary, advice string) {
	key := retainedText(code)
	if retainedServerCode.MatchString(key) {
		key = "provider_http_5xx"
	}
	if value := retainedCatalogLookup(retainedErrors, key); retainedTruthy(code) && retainedTruthy(value) {
		entry := retainedObject(value)
		return retainedText(retainedGet(entry, "summary")), retainedText(retainedGet(entry, "advice"))
	}
	if retainedHTTPCode.MatchString(retainedText(code)) {
		return strings.ReplaceAll(retainedWord("httpSummary"), "{status}", retainedText(code)[len(retainedText(code))-3:]), retainedWord("httpAdvice")
	}
	fallback := retainedObject(retainedGet(retainedErrors, "unknown_error"))
	reason := retainedCatalogLookup(retainedObject(retainedGet(retainedFlow, "reasonLabels")), retainedText(code))
	return retainedText(retainedCoalesce(reason, retainedGet(fallback, "summary"))), retainedText(retainedGet(fallback, "advice"))
}
func retainedDiagnosticLines(log *jsjson.Object) []string {
	rootValue := retainedGet(log, "rootCause")
	if rootValue == nil || jsjson.IsUndefined(rootValue) {
		if retainedTruthy(retainedGet(log, "errorType")) || retainedTruthy(retainedGet(log, "errorCode")) || retainedTruthy(retainedGet(log, "errorMessage")) {
			rootValue = log
		} else if retainedGet(log, "level") == "error" || retainedGet(log, "status") == "failed" {
			rootValue, _ = jsjson.NewObject(jsjson.Member{Key: "errorType", Value: "UnknownError"}, jsjson.Member{Key: "errorCode", Value: "cause_not_captured"})
		}
	}
	if !retainedTruthy(rootValue) {
		return nil
	}
	root := retainedObject(rootValue)
	code := retainedCoalesce(retainedGet(root, "errorCode"), retainedGet(root, "systemCode"))
	if code == nil || jsjson.IsUndefined(code) {
		if retainedTruthy(retainedGet(root, "httpStatus")) {
			code = "provider_http_" + retainedText(retainedGet(root, "httpStatus"))
		}
	}
	summary, _ := retainedLocalized(code)
	context := new(jsjson.Object)
	for _, key := range []string{"operation", "stage", "role", "modelId", "providerId", "apiModel"} {
		value := retainedGet(log, key)
		if key == "stage" {
			value = retainedCoalesce(value, retainedGet(log, "phase"))
		}
		context.Set(key, value)
	}
	for _, object := range []*jsjson.Object{retainedObject(retainedGet(log, "errorContext")), retainedObject(retainedGet(root, "errorContext"))} {
		if object != nil {
			for _, member := range object.Members() {
				context.Set(member.Key, member.Value)
			}
		}
	}
	first := retainedWord("root") + ": " + summary
	if retainedTruthy(code) {
		first += " [" + retainedText(code) + "]"
	}
	result := []string{first}
	appendTruthy := func(o *jsjson.Object, key, label string) {
		if value := retainedGet(o, key); retainedTruthy(value) {
			result = append(result, label+"="+retainedText(value))
		}
	}
	appendPresent := func(o *jsjson.Object, key, label, suffix string) {
		if value := retainedGet(o, key); !jsjson.IsUndefined(value) {
			result = append(result, label+"="+retainedText(value)+suffix)
		}
	}
	appendTruthy(root, "errorType", retainedWord("type"))
	message := retainedGet(root, "errorMessage")
	if retainedTruthy(message) && !retainedSame(message, code) && !retainedSame(message, summary) {
		result = append(result, retainedWord("technical")+": "+retainedText(message))
	}
	appendPresent(root, "httpStatus", "HTTP", "")
	appendTruthy(root, "systemCode", retainedWord("system"))
	appendTruthy(context, "operation", retainedWord("operation"))
	if value := retainedGet(context, "stage"); retainedTruthy(value) {
		result = append(result, retainedWord("stage")+"="+retainedLabel(retainedFlow, "stageLabels", value))
	}
	if value := retainedGet(context, "role"); retainedTruthy(value) {
		result = append(result, retainedWord("role")+"="+retainedLabel(retainedPresentation, "roles", value))
	}
	if retainedTruthy(retainedGet(context, "apiModel")) || retainedTruthy(retainedGet(context, "modelId")) {
		result = append(result, retainedWord("model")+"="+retainedText(retainedCoalesce(retainedGet(context, "apiModel"), retainedGet(context, "modelId"))))
	}
	appendTruthy(context, "providerId", retainedWord("provider"))
	for _, f := range []struct{ key, label, suffix string }{{"attempt", "attempt", ""}, {"maxAttempts", "maxAttempts", ""}, {"stageBudgetMs", "stageBudget", "ms"}, {"timeoutMs", "timeout", "ms"}, {"remainingCandidates", "remaining", ""}} {
		appendPresent(context, f.key, retainedWord(f.label), f.suffix)
	}
	if value := retainedGet(context, "retryable"); !jsjson.IsUndefined(value) {
		word := "no"
		if retainedTruthy(value) {
			word = "yes"
		}
		result = append(result, retainedWord("retryable")+"="+retainedWord(word))
	}
	_, advice := retainedLocalized(retainedCoalesce(retainedGet(root, "adviceCode"), code))
	result = append(result, retainedWord("advice")+": "+advice)
	return result
}
func retainedReadable(log *jsjson.Object) string {
	get := func(key string) any { return retainedGet(log, key) }
	first := ""
	if retainedTruthy(get("stage")) && retainedTruthy(get("status")) {
		first = retainedLabel(retainedFlow, "statusLabels", get("status")) + ": " + retainedLabel(retainedFlow, "stageLabels", get("stage"))
	} else {
		parts := []string{}
		for _, key := range []string{"module", "code"} {
			if retainedTruthy(get(key)) {
				parts = append(parts, retainedText(get(key)))
			}
		}
		first = strings.Join(parts, " · ")
	}
	result := []string{first}
	if retainedTruthy(get("role")) {
		result = append(result, retainedWord("role")+"="+retainedLabel(retainedPresentation, "roles", get("role")))
	}
	if retainedTruthy(get("apiModel")) || retainedTruthy(get("modelId")) {
		result = append(result, retainedWord("model")+"="+retainedText(retainedCoalesce(get("apiModel"), get("modelId"))))
	}
	for _, f := range []struct{ key, label string }{{"providerId", "provider"}, {"toolName", "tool"}} {
		if retainedTruthy(get(f.key)) {
			result = append(result, retainedWord(f.label)+"="+retainedText(get(f.key)))
		}
	}
	if retainedTruthy(get("reason")) {
		reason := retainedCatalogLookup(retainedObject(retainedGet(retainedFlow, "reasonLabels")), retainedText(get("reason")))
		if reason == nil || jsjson.IsUndefined(reason) {
			if retainedHTTPCode.MatchString(retainedText(get("reason"))) {
				reason, _ = retainedLocalized(get("reason"))
			} else {
				reason = retainedWord("unknown") + " (" + retainedText(get("reason")) + ")"
			}
		}
		result = append(result, retainedWord("reason")+": "+retainedText(reason))
	}
	if counts := retainedObject(get("counts")); counts != nil {
		for _, member := range counts.Members() {
			label := retainedLabel(retainedFlow, "countLabels", member.Key)
			if get("mode") == "simulation" && member.Key == "sentSegments" {
				label = retainedWord("simulated")
			}
			result = append(result, label+"="+retainedText(member.Value))
		}
	}
	if retainedTruthy(get("outputKind")) {
		result = append(result, retainedWord("result")+"="+retainedLabel(retainedPresentation, "outputs", get("outputKind")))
	}
	if !jsjson.IsUndefined(get("durationMs")) {
		result = append(result, retainedWord("duration")+"="+retainedText(get("durationMs"))+"ms")
	}
	lines := retainedDiagnosticLines(log)
	if len(lines) > 0 {
		result = append(result, lines...)
	} else {
		if retainedTruthy(get("systemCode")) {
			result = append(result, retainedWord("system")+"="+retainedText(get("systemCode")))
		}
		if !jsjson.IsUndefined(get("httpStatus")) {
			result = append(result, "HTTP="+retainedText(get("httpStatus")))
		}
	}
	if retainedTruthy(get("nextStep")) {
		result = append(result, retainedWord("next")+": "+retainedLabel(retainedFlow, "nextLabels", get("nextStep")))
	}
	nonempty := result[:0]
	for _, part := range result {
		if part != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "；")
}
