package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/hsmade/go-ft8-reporter/pkg/reporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const metricPrefix = "go_ft8_reporter_"

type appMetrics struct {
	registry                                            *prometheus.Registry
	reporter                                            *reporter.Reporter // assigned during startup, before serving metrics
	slots, messages, ldpc                               *prometheus.CounterVec
	candidates, dropped, iqSamples, readErrors, skipped prometheus.Counter
	duration                                            prometheus.Histogram
	reportingEnabled                                    prometheus.Gauge
}

func newMetrics(mode string) *appMetrics {
	m := &appMetrics{registry: prometheus.NewRegistry()}
	labels := prometheus.Labels{"mode": mode}
	counter := func(name, help string) prometheus.Counter {
		c := prometheus.NewCounter(prometheus.CounterOpts{Name: metricPrefix + name, Help: help, ConstLabels: labels})
		m.registry.MustRegister(c)
		return c
	}
	vec := func(name, help, label string, values []string) *prometheus.CounterVec {
		c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: metricPrefix + name, Help: help, ConstLabels: labels}, []string{label})
		m.registry.MustRegister(c)
		for _, v := range values {
			c.WithLabelValues(v)
		}
		return c
	}
	m.slots = vec("decode_slots_total", "Completed decode calls by outcome; empty is not an error.", "result", []string{"success", "empty", "error"})
	m.messages = vec("decoded_messages_total", "Unique CRC-verified messages returned per slot, including unparsed payloads.", "status", []string{"parsed", "unsupported", "unknown_type", "invalid", "unset"})
	m.ldpc = vec("ldpc_attempts_total", "LDPC decode attempts; failed candidates need not represent actual transmissions.", "result", []string{"success", "failure"})
	m.candidates = counter("decode_candidates_total", "Coarse synchronization candidates found by the decoder.")
	m.dropped = counter("dropped_slots_total", "Complete live slots dropped because the decoder queue was full.")
	m.iqSamples = counter("iq_samples_total", "Complex IQ samples read from the source before decimation.")
	m.readErrors = counter("iq_read_errors_total", "Input read failures excluding EOF and intentional shutdown.")
	m.skipped = counter("reports_skipped_total", "Decoded messages skipped because a safe transmitter report could not be extracted.")
	m.duration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: metricPrefix + "decode_duration_seconds", Help: "Wall time spent decoding a slot.", ConstLabels: labels, Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15}})
	m.reportingEnabled = prometheus.NewGauge(prometheus.GaugeOpts{Name: metricPrefix + "reporting_enabled", Help: "Whether live PSK Reporter uploads are enabled (0 or 1).", ConstLabels: labels})
	m.registry.MustRegister(m.duration, m.reportingEnabled, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	stats := func() reporter.Stats {
		if m.reporter == nil {
			return reporter.Stats{}
		}
		return m.reporter.Stats()
	}
	for _, item := range []struct {
		name, help string
		value      func(reporter.Stats) float64
	}{
		{"reports_queued_total", "Reports accepted into the exporter queue.", func(s reporter.Stats) float64 { return float64(s.Queued) }},
		{"reports_duplicate_total", "Reports suppressed by the exporter's callsign/band/mode deduplication.", func(s reporter.Stats) float64 { return float64(s.Duplicates) }},
		{"reports_rejected_total", "Report Add calls rejected due to validation, closure or send failures.", func(s reporter.Stats) float64 { return float64(s.Rejected) }},
		{"reports_exported_total", "Reception reports written successfully to UDP; collector receipt is not acknowledged.", func(s reporter.Stats) float64 { return float64(s.Exported) }},
		{"export_datagrams_total", "Successful UDP datagram writes.", func(s reporter.Stats) float64 { return float64(s.Datagrams) }},
		{"export_bytes_total", "Bytes written in successful UDP datagrams.", func(s reporter.Stats) float64 { return float64(s.Bytes) }},
		{"export_errors_total", "UDP send attempts that failed, including deadline and short-write failures.", func(s reporter.Stats) float64 { return float64(s.SendErrors) }},
		{"reports_discarded_total", "Pending reports discarded when the exporter closed.", func(s reporter.Stats) float64 { return float64(s.Discarded) }},
	} {
		m.registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: metricPrefix + item.name, Help: item.help, ConstLabels: labels}, func() float64 { return item.value(stats()) }))
	}
	m.registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metricPrefix + "reports_pending", Help: "Reception reports currently waiting for export.", ConstLabels: labels}, func() float64 { return float64(stats().Pending) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metricPrefix + "last_export_timestamp_seconds", Help: "Unix timestamp of the last successful UDP write, or zero.", ConstLabels: labels}, func() float64 { return float64(stats().LastExportUnix) }),
	)
	return m
}

func (m *appMetrics) observeDecode(messages []ft8.DecodedMessage, err error, duration time.Duration, candidates, attempts, failures int) {
	if m == nil {
		return
	}
	m.duration.Observe(duration.Seconds())
	m.candidates.Add(float64(candidates))
	m.ldpc.WithLabelValues("failure").Add(float64(failures))
	m.ldpc.WithLabelValues("success").Add(float64(max(0, attempts-failures)))
	result := "success"
	if err != nil {
		result = "error"
	} else if len(messages) == 0 {
		result = "empty"
	}
	m.slots.WithLabelValues(result).Inc()
	if err == nil {
		for _, msg := range messages {
			m.messages.WithLabelValues(msg.ParseStatus.String()).Inc()
		}
	}
}

func startMetrics(address string, m *appMetrics, logger *log.Logger) (func(), error) {
	if address == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("metrics server: %v", err)
		}
	}()
	logger.Printf("Prometheus metrics listening on %s/metrics", listener.Addr())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Printf("metrics shutdown: %v", err)
			_ = server.Close()
		}
	}, nil
}
