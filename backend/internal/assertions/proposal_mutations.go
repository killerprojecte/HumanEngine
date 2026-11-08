package assertions

// RetireProposalFingerprint is the source's bookkeeping-only fingerprint move
// before a revert insert. It deliberately does not create a revision or remove
// a projection: the old historical statement itself has not changed.
func (s *Store) RetireProposalFingerprint(record *Object) error {
	fp, e := HistoricalFingerprint(record)
	if e != nil {
		return e
	}
	return s.exec("UPDATE memories SET fingerprint=? WHERE id=?", fp, str(get(record, "id")))
}
