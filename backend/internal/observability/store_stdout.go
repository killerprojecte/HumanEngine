package observability

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"strings"
)

// sourceStdout uses the exact already-serialized flat row, never another JSON
// encode of caller data. A non-string level fails in the source admission try
// block after retention/acquisition, not in the serialization error branch.
func sourceStdout(row *jsjson.Object, encoded []byte) (string, error) {
	level, ok := retainedGet(row, "level").(string)
	if !ok {
		return "", errors.New("log_stdout_level_invalid")
	}
	correlation := make([]string, 0, 3)
	for _, key := range []string{"traceId", "turnId", "batchId"} {
		value := retainedGet(row, key)
		if retainedTruthy(value) {
			label := strings.TrimSuffix(key, "Id")
			correlation = append(correlation, retainedWord(label)+"="+retainedText(value))
		}
	}
	prefix := retainedText(retainedGet(row, "at")) + " " + cases.Upper(language.Und).String(level) + " "
	if len(correlation) > 0 {
		prefix += "[" + strings.Join(correlation, " ") + "] "
	}
	// Source recomputes readableLog, which is pure over this owned snapshot.
	return prefix + retainedReadable(row) + " | " + string(encoded), nil
}
