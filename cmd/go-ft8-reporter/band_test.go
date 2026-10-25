package main

import (
	"testing"
	"time"
)

func TestBandChannelValidation(t *testing.T) {
	good, err := parseBandChannels([]string{"ft8:144174000:9210", "ft4:144170000:9211"})
	if err != nil || len(good) != 2 {
		t.Fatal(good, err)
	}
	for _, args := range [][]string{{}, {"ft8:0:9210"}, {"bad:144174000:9210"}, {"ft8:144174000:9210", "ft4:144170000:9210"}, {"ft8:144174000:9210", "ft8:144170000:9211"}, {"ft4:144170000:70000"}} {
		if _, err := parseBandChannels(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestBandMetricsRemainIndependent(t *testing.T) {
	ft8, ft4 := newMetrics("ft8"), newMetrics("ft4")
	ft8.observeDecode(nil, nil, time.Millisecond, 5, 7, 2)
	if metricValue(t, ft8, "decode_slots_total", "result", "empty") != 1 || metricValue(t, ft4, "decode_slots_total", "result", "empty") != 0 {
		t.Fatal("channel metrics leaked")
	}
	if metricValue(t, ft4, "decode_candidates_total", "", "") != 0 {
		t.Fatal("candidate metrics leaked")
	}
}
