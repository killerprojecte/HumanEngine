package assertionauthority

import (
	"regexp"
	"strconv"
)

var trustedPattern = regexp.MustCompile(`^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(?:Z|[+-](\d\d):(\d\d))$`)

func (a *Authority) trustedTime(value any) (string, int64, bool, error) {
	s, ok := value.(string)
	if !ok {
		return "", 0, false, nil
	}
	m := trustedPattern.FindStringSubmatch(s)
	if m == nil {
		return "", 0, false, nil
	}
	ms, valid, e := a.options.DateParser.ParseMillis(s)
	if e != nil || !valid {
		return "", 0, false, e
	}
	n := make([]int, 8)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	days := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if n[0]%4 == 0 && (n[0]%100 != 0 || n[0]%400 == 0) {
		days[1] = 29
	}
	if n[1] < 1 || n[1] > 12 || n[2] < 1 || n[2] > days[n[1]-1] || n[3] > 23 || n[4] > 59 || n[5] > 59 || n[6] > 23 || n[7] > 59 {
		return "", 0, false, nil
	}
	return s, ms, true, nil
}
func (a *Authority) WithObservation(memory *Object, proofs []any) (*Object, error) {
	var earliest string
	var earliestMS int64
	for _, proof := range proofs {
		if get(proof, "sourceObservations") == nil {
			continue
		}
		observations, e := fieldArray(proof, "sourceObservations")
		if e != nil {
			return nil, e
		}
		for _, observation := range observations {
			s, ms, valid, e := a.trustedTime(get(observation, "receivedAt"))
			if e != nil {
				return nil, e
			}
			if !valid {
				continue
			}
			before := earliest == "" || ms < earliestMS
			if earliest != "" && ms == earliestMS {
				order, e := a.options.Compare(s, earliest)
				if e != nil {
					return nil, e
				}
				before = order < 0
			}
			if before {
				earliest, earliestMS = s, ms
			}
		}
	}
	record := memory.Clone()
	record.Delete("observedAt")
	if earliest != "" {
		_ = record.Set("observedAt", earliest)
	}
	return record, nil
}
