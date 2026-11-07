package recovery

import (
	"regexp"
	"strconv"
)

var opaqueReceiptPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var calendarDatePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
var utcInstantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?Z$`)
var offsetInstantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func opaqueReceiptID(value any) bool {
	text, ok := value.(string)
	return ok && opaqueReceiptPattern.MatchString(text)
}

func validCalendarDate(value any) bool {
	text, ok := value.(string)
	if !ok || !calendarDatePattern.MatchString(text) {
		return false
	}
	year, _ := strconv.Atoi(text[:4])
	month, _ := strconv.Atoi(text[5:7])
	day, _ := strconv.Atoi(text[8:10])
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days[1] = 29
	}
	return day <= days[month-1]
}

// z.iso.datetime is a lexical calendar check, not time.Parse(RFC3339): it
// requires seconds, allows arbitrarily long fractional seconds and, when
// offset is enabled, accepts offsets through 23:59. Year zero is valid.
func validContractInstant(value any, offset bool) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	pattern := utcInstantPattern
	if offset {
		pattern = offsetInstantPattern
	}
	return pattern.MatchString(text) && validCalendarDate(text[:10])
}

func validExternalCost(event map[string]any) bool {
	if !exactKeys(event, []string{"kind", "receipt"}) {
		return false
	}
	receipt, ok := event["receipt"].(map[string]any)
	if !ok || !exactKeys(receipt, []string{"schemaVersion", "revision", "receipt", "correctionId", "recordedAt"}) ||
		receipt["schemaVersion"] != float64(1) || !positiveVersion(receipt["revision"]) || receipt["revision"].(float64) > 1000 ||
		(receipt["correctionId"] != nil && !opaqueReceiptID(receipt["correctionId"])) || !validContractInstant(receipt["recordedAt"], false) ||
		(receipt["revision"] == float64(1)) != (receipt["correctionId"] == nil) {
		return false
	}
	input, ok := receipt["receipt"].(map[string]any)
	if !ok || !opaqueReceiptID(input["id"]) || !opaqueReceiptID(input["sourceId"]) ||
		!oneOf(input["category"], "tool", "storage", "compute") || input["currency"] != "USD" ||
		!validCalendarDate(input["billingDate"]) || !validCalendarDate(input["periodStart"]) || !validCalendarDate(input["periodEnd"]) ||
		input["periodStart"].(string) > input["periodEnd"].(string) {
		return false
	}
	keys := []string{"id", "category", "currency", "billingDate", "periodStart", "periodEnd", "sourceId", "status"}
	switch input["status"] {
	case "known":
		return exactKeys(input, append(keys, "amountNanoUsd")) && integer(input["amountNanoUsd"])
	case "unknown":
		return exactKeys(input, keys)
	default:
		return false
	}
}

func validCommitment(event map[string]any) bool {
	if !codePointID(event["id"], 512) || !codePointID(event["sceneId"], 512) || !integer(event["deletionEpoch"]) {
		return false
	}
	common := []string{"kind", "schemaVersion", "id", "sceneId", "deletionEpoch"}
	if event["kind"] == "commitment_delete" {
		return event["schemaVersion"] == float64(1) && exactKeys(event, common)
	}
	if !exactKeys(event, append(common, "termsVersion", "mutationVersion", "qualificationHash"), "temporalVersion", "privacyMarker") ||
		(event["schemaVersion"] != float64(1) && event["schemaVersion"] != float64(2)) ||
		!positiveVersion(event["termsVersion"]) || !positiveVersion(event["mutationVersion"]) || !hashString(event["qualificationHash"]) {
		return false
	}
	temporal, hasTemporal := event["temporalVersion"]
	if (hasTemporal && temporal != float64(1)) || (event["schemaVersion"] == float64(2)) != hasTemporal {
		return false
	}
	markerValue, hasMarker := event["privacyMarker"]
	if !hasMarker {
		return true
	}
	marker, ok := markerValue.(map[string]any)
	if !ok || event["schemaVersion"] != float64(2) || !exactKeys(marker, []string{"recordedAt", "head", "reason"}) ||
		!validContractInstant(marker["recordedAt"], true) || !oneOf(marker["reason"], "purge", "requalify") {
		return false
	}
	head, ok := marker["head"].(map[string]any)
	if !ok || !validCommitmentMarkerHead(head) {
		return false
	}
	return head["id"] == event["id"] && head["sceneId"] == event["sceneId"] &&
		head["mutationVersion"] == event["mutationVersion"] && head["latestVersion"] == event["termsVersion"] && head["deletionEpoch"] == event["deletionEpoch"]
}

func validCommitmentMarkerHead(head map[string]any) bool {
	if !exactKeys(head, []string{"id", "sceneId", "latestVersion", "activeVersion", "everActivatedVersion", "everTerminal", "deletionEpoch", "mutationVersion", "deleted"}) ||
		!codePointID(head["id"], 512) || !codePointID(head["sceneId"], 512) || !positiveVersion(head["latestVersion"]) ||
		!nullableVersion(head["activeVersion"]) || !nullableVersion(head["everActivatedVersion"]) ||
		!integer(head["deletionEpoch"]) || !positiveVersion(head["mutationVersion"]) || head["deleted"] != false {
		return false
	}
	latest := head["latestVersion"].(float64)
	for _, key := range []string{"activeVersion", "everActivatedVersion"} {
		if value := head[key]; value != nil && value.(float64) > latest {
			return false
		}
	}
	if value := head["everTerminal"]; value != nil {
		terminal, ok := value.(map[string]any)
		if !ok || !exactKeys(terminal, []string{"version", "state"}) || !positiveVersion(terminal["version"]) ||
			terminal["version"].(float64) > latest || !oneOf(terminal["state"], "done", "cancelled") {
			return false
		}
	}
	return true
}
