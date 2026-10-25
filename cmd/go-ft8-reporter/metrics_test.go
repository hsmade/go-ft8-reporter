package main

import (
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func metricValue(t *testing.T, m *appMetrics, name, label, value string) float64 {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != metricPrefix+name {
			continue
		}
		for _, metric := range family.Metric {
			match := label == ""
			for _, l := range metric.Label {
				if l.GetName() == label && l.GetValue() == value {
					match = true
				}
			}
			if match {
				if metric.Counter != nil {
					return metric.Counter.GetValue()
				}
				if metric.Gauge != nil {
					return metric.Gauge.GetValue()
				}
				if metric.Histogram != nil {
					return float64(metric.Histogram.GetSampleCount())
				}
			}
		}
	}
	t.Fatalf("metric missing: %s{%s=%q}", name, label, value)
	return 0
}

func TestMetricsExposition(t *testing.T) {
	m := newMetrics("ft8")
	m.observeDecode(nil, nil, time.Millisecond, 0, 0, 0)
	m.observeDecode(nil, errors.New("invalid input"), time.Millisecond, 0, 0, 0)
	m.observeDecode([]ft8.DecodedMessage{{ParseStatus: ft8.ParseStatusParsed}, {ParseStatus: ft8.ParseStatusUnsupported}}, nil, time.Second, 7, 5, 3)
	for _, test := range []struct {
		name, label, value string
		want               float64
	}{
		{"decode_slots_total", "result", "success", 1},
		{"decode_slots_total", "result", "empty", 1},
		{"decode_slots_total", "result", "error", 1},
		{"decoded_messages_total", "status", "parsed", 1},
		{"decoded_messages_total", "status", "unsupported", 1},
		{"ldpc_attempts_total", "result", "failure", 3},
		{"ldpc_attempts_total", "result", "success", 2},
		{"decode_candidates_total", "", "", 7},
		{"decode_duration_seconds", "", "", 3},
		{"reports_exported_total", "", "", 0},
		{"reporting_enabled", "", "", 0},
	} {
		if got := metricValue(t, m, test.name, test.label, test.value); got != test.want {
			t.Fatalf("%s: %g, want %g", test.name, got, test.want)
		}
	}
	w := httptest.NewRecorder()
	promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("invalid scrape: %d %s", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), `go_ft8_reporter_decoded_messages_total{mode="ft8",status="parsed"} 1`) {
		t.Fatal(w.Body.String())
	}
	closeServer, err := startMetrics("", m, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	closeServer()
}

func TestDecodeSlotMetrics(t *testing.T) {
	for _, mode := range []string{"ft8", "ft4"} {
		size := 180000
		if mode == "ft4" {
			size = 90000
		}
		jobs := make(chan receiveSlot, 2)
		jobs <- receiveSlot{pcm: nil}                 // invalid length: checked decoder must error
		jobs <- receiveSlot{pcm: make([]int16, size)} // valid silence: empty
		close(jobs)
		m := newMetrics(mode)
		decodeSlots(mode, jobs, log.New(io.Discard, "", 0), nil, m)
		if metricValue(t, m, "decode_slots_total", "result", "error") != 1 || metricValue(t, m, "decode_slots_total", "result", "empty") != 1 {
			t.Fatal("decode errors and empty slots were not counted separately")
		}
	}
}
