package reporter

// Stats is a consistent snapshot. Exported counts successful local UDP writes,
// not acknowledgements from PSK Reporter (the protocol has none).
type Stats struct {
	Queued, Duplicates, Rejected                      uint64
	Exported, Datagrams, Bytes, SendErrors, Discarded uint64
	Pending                                           int
	LastExportUnix                                    int64
}

func (r *Reporter) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Pending = len(r.records)
	if !r.lastSend.IsZero() {
		s.LastExportUnix = r.lastSend.Unix()
	}
	return s
}
