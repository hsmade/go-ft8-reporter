package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/hsmade/go-ft8-reporter/pkg/reporter"
	"github.com/hsmade/go-ft8-reporter/pkg/sdr"
)

type channelFlags []string

func (c *channelFlags) String() string         { return strings.Join(*c, ",") }
func (c *channelFlags) Set(value string) error { *c = append(*c, value); return nil }

type bandChannel struct {
	mode        string
	dial        uint64
	metricsPort int
}

func parseBandChannels(args []string) ([]bandChannel, error) {
	channels := make([]bandChannel, 0, len(args))
	ports := map[int]bool{}
	modes := map[string]bool{}
	for _, arg := range args {
		parts := strings.Split(arg, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("channel must be MODE:DIAL_HZ:METRICS_PORT")
		}
		dial, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || dial == 0 || dial >= (1<<40)-6000 {
			return nil, fmt.Errorf("invalid channel dial frequency")
		}
		port, err := strconv.Atoi(parts[2])
		if err != nil || port < 1 || port > 65535 || ports[port] {
			return nil, fmt.Errorf("channel metrics ports must be valid and unique")
		}
		if (parts[0] != "ft8" && parts[0] != "ft4") || modes[parts[0]] {
			return nil, fmt.Errorf("use at most one ft8 and one ft4 channel per band")
		}
		channels = append(channels, bandChannel{parts[0], dial, port})
		ports[port] = true
		modes[parts[0]] = true
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("at least one channel is required")
	}
	return channels, nil
}

// CPU profiles are opt-in files, never an exposed profiling HTTP endpoint.
func startCPUProfile(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err = pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { pprof.StopCPUProfile(); f.Close() }, nil
}

func channelReporter(c bandChannel, m *appMetrics, logger *log.Logger) (func(time.Time, ft8.DecodedMessage), func(), error) {
	if *noReport || *iqFile != "" {
		logger.Printf("PSK Reporter disabled mode=%s", c.mode)
		return nil, func() {}, nil
	}
	r, err := reporter.New(reporter.Config{Receiver: reporter.Receiver{Callsign: *receiverCall, Locator: *receiverGrid, Software: "go-ft8-reporter (go-ft8 v0.9.0)", Antenna: *antenna}, Address: *reportAddress})
	if err != nil {
		return nil, nil, err
	}
	m.reporter = r
	m.reportingEnabled.Set(1)
	go func() {
		for err := range r.Errors() {
			logger.Printf("PSK Reporter mode=%s: %v", c.mode, err)
		}
	}()
	cleanup := func() {
		if err := r.Flush(); err != nil {
			logger.Printf("PSK Reporter mode=%s final flush: %v", c.mode, err)
		}
		if err := r.Close(); err != nil {
			logger.Printf("PSK Reporter mode=%s close: %v", c.mode, err)
		}
	}
	callback := func(slot time.Time, msg ft8.DecodedMessage) {
		spot, ok := spotFromMessage(c.mode, slot, msg, c.dial)
		if !ok {
			m.skipped.Inc()
			return
		}
		if _, err := r.Add(spot); err != nil {
			logger.Printf("PSK Reporter mode=%s add %s: %v", c.mode, spot.Callsign, err)
		}
	}
	logger.Printf("PSK Reporter enabled mode=%s receiver=%s locator=%s dial=%dHz", c.mode, *receiverCall, *receiverGrid, c.dial)
	return callback, cleanup, nil
}

func runBand(logger *log.Logger) error {
	channels, err := parseBandChannels(multiChannels)
	if err != nil {
		return err
	}
	if *centerFrequency == 0 {
		return fmt.Errorf("--center-frequency is required with --channel")
	}
	if *sampleRate != float64(int(*sampleRate)) {
		return fmt.Errorf("shared input sample rate must be integral")
	}
	dials := make([]float64, len(channels))
	for i, c := range channels {
		dials[i] = float64(c.dial)
	}
	frontend, err := sdr.NewBandFrontend(int(*sampleRate), float64(*centerFrequency), dials, blockSize)
	if err != nil {
		return err
	}
	metrics := make([]*appMetrics, len(channels))
	jobs := make([]chan receiveSlot, len(channels))
	collectors := make([]*slotCollector, len(channels))
	// Close decode queues and drain workers before flushing reporters.
	done := make(chan struct{}, len(channels))
	callbacks := make([]func(time.Time, ft8.DecodedMessage), len(channels))
	for i, c := range channels {
		m := newMetrics(c.mode)
		metrics[i] = m
		callback, cleanup, err := channelReporter(c, m, logger)
		if err != nil {
			return err
		}
		defer cleanup()
		closeMetrics, err := startMetrics(fmt.Sprintf(":%d", c.metricsPort), m, logger)
		if err != nil {
			return err
		}
		defer closeMetrics()
		callbacks[i] = callback
	}
	for i, c := range channels {
		jobs[i] = make(chan receiveSlot, 2)
		go func(c bandChannel, q chan receiveSlot, m *appMetrics, callback func(time.Time, ft8.DecodedMessage)) {
			decodeSlots(c.mode, q, logger, callback, m)
			done <- struct{}{}
		}(c, jobs[i], metrics[i], callbacks[i])
	}

	defer func() {
		for _, q := range jobs {
			close(q)
		}
		for range jobs {
			<-done
		}
	}()
	var source interface {
		ReadIQ([]complex64) (int, error)
	}
	var closer io.Closer
	var captureStart time.Time
	if *iqFile != "" {
		captureStart, err = time.Parse(time.RFC3339Nano, *startTime)
		if err != nil {
			return fmt.Errorf("--iq-file requires --start-time: %w", err)
		}
		file, e := os.Open(*iqFile)
		if e != nil {
			return e
		}
		source = sdr.NewU8IQReader(file, blockSize)
		closer = file
	} else {
		if *startTime != "" {
			return fmt.Errorf("--start-time requires --iq-file")
		}
		tcp, e := sdr.DialRTLTCP(*addr, blockSize)
		if e != nil {
			return e
		}
		source = tcp
		closer = tcp
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer closer.Close()
	go func() { <-ctx.Done(); closer.Close() }()
	input := make([]complex64, blockSize)
	output := make([][]complex64, len(channels))
	for {
		n, readErr := source.ReadIQ(input)
		if n > 0 {
			first := captureStart
			if first.IsZero() {
				first = time.Now().UTC().Add(-*streamLatency).Add(-time.Duration(float64(n) / *sampleRate * float64(time.Second)))
			}
			frontend.Process(input[:n], output)
			for i, c := range channels {
				metrics[i].iqSamples.Add(float64(n)) // shared source samples seen by this channel
				if collectors[i] == nil {
					period := 15 * time.Second
					if c.mode == "ft4" {
						period = 7500 * time.Millisecond
					}
					collectors[i] = newSlotCollector(first.Add(time.Duration(float64(frontend.DelaySamples(i)) / *sampleRate * float64(time.Second))), period)
					logger.Printf("shared receiving mode=%s dial=%d first_slot=%s", c.mode, c.dial, collectors[i].start.Format(time.RFC3339Nano))
				}
				collectors[i].push(output[i], func(slot receiveSlot) {
					select {
					case jobs[i] <- slot:
					default:
						if *iqFile != "" {
							jobs[i] <- slot
						} else {
							metrics[i].dropped.Inc()
							logger.Printf("dropping mode=%s slot=%s: decoder queue full", c.mode, slot.start.Format(time.RFC3339Nano))
						}
					}
				})
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || ctx.Err() != nil {
				return nil
			}
			for _, m := range metrics {
				m.readErrors.Inc()
			}
			return fmt.Errorf("shared input stopped: %w", readErr)
		}
	}
}
