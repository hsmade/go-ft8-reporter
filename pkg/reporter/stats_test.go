package reporter

import (
	"errors"
	"testing"
)

func TestExportStats(t *testing.T) {
	r, c, _ := newTestReporter(t)
	spot := testSpot()
	_, _ = r.Add(spot)
	_, _ = r.Add(spot)
	bad := spot
	bad.FrequencyHz = 0
	_, _ = r.Add(bad)
	s := r.Stats()
	if s.Queued != 1 || s.Duplicates != 1 || s.Rejected != 1 || s.Pending != 1 || s.Exported != 0 {
		t.Fatalf("queue stats %+v", s)
	}
	c.err = errors.New("offline")
	if err := r.Flush(); err == nil {
		t.Fatal("expected failure")
	}
	s = r.Stats()
	if s.SendErrors != 1 || s.Pending != 1 || s.Exported != 0 || s.Datagrams != 0 {
		t.Fatalf("failure stats %+v", s)
	}
	c.err = nil
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	s = r.Stats()
	if s.Exported != 1 || s.Datagrams != 1 || s.Pending != 0 || s.Bytes != uint64(len(c.packets[0])) || s.LastExportUnix == 0 {
		t.Fatalf("export stats %+v", s)
	}
	spot.Callsign = "W9XYZ"
	_, _ = r.Add(spot)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	s = r.Stats()
	if s.Discarded != 1 || s.Pending != 0 || s.Queued != 2 {
		t.Fatalf("close stats %+v", s)
	}
}
