package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/hsmade/go-ft8-reporter/pkg/reporter"
	"github.com/hsmade/go-ft8-reporter/pkg/sdr"
)

var (
	addr           = flag.String("addr", "localhost:5555", "rtl_tcp proxy address")
	sampleRate     = flag.Float64("sample-rate", 2.4e6, "IQ sample rate (1000000 Hz or a multiple of 12000 Hz)")
	shiftRate      = flag.Float64("shift", 0, "frequency shift relative to sample rate")
	mode           = flag.String("mode", "ft8", "Mode (ft8, ft4)")
	blockSize      = 4096
	streamLatency  = flag.Duration("stream-latency", 0, "estimated source/network buffering delay, subtracted from receive time")
	iqFile         = flag.String("iq-file", "", "read raw unsigned 8-bit interleaved IQ from a file instead of TCP")
	startTime      = flag.String("start-time", "", "UTC capture time of first file sample (RFC3339; required with --iq-file)")
	noReport       = flag.Bool("no-report", false, "disable PSK Reporter uploads (always disabled for file replay)")
	receiverCall   = flag.String("callsign", "", "receiver callsign for PSK Reporter")
	receiverGrid   = flag.String("locator", "", "receiver Maidenhead locator for PSK Reporter")
	antenna        = flag.String("antenna", "", "receiving antenna description")
	dialFrequency  = flag.Uint64("dial-frequency", 0, "tuned USB dial frequency in Hz, required for reporting")
	reportAddress  = flag.String("reporter-address", reporter.DefaultAddress, "PSK Reporter UDP destination")
	metricsAddress = flag.String("metrics-address", ":2112", "Prometheus HTTP listen address; empty disables the endpoint")
)

var multiChannels channelFlags
var centerFrequency = flag.Uint64("center-frequency", 0, "source center frequency in Hz for shared channels")
var cpuProfile = flag.String("cpu-profile", "", "write CPU profile until shutdown (optional)")

func init() {
	flag.Var(&multiChannels, "channel", "shared channel MODE:DIAL_HZ:METRICS_PORT (repeat for FT8/FT4)")
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	flag.Parse()
	logger := log.New(os.Stderr, "", log.Ldate|log.Ltime|log.LUTC)
	cleanup, err := startCPUProfile(*cpuProfile)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(multiChannels) > 0 {
		return runBand(logger)
	}
	runSingle(logger)
	return nil
}

func runSingle(logger *log.Logger) {
	if *mode != "ft8" && *mode != "ft4" {
		log.Fatal("mode must be ft8 or ft4")
	}

	// 1 MHz needs rational conversion: 1 MHz -> 25 kHz -> 12 kHz.
	intermediateRate := float64(pcmRate)
	var resampler *sdr.RationalResampler
	if *sampleRate == 1000000 {
		intermediateRate = 25000
		resampler, _ = sdr.NewRationalResampler(12, 25)
	}
	if math.IsNaN(*sampleRate) || math.IsInf(*sampleRate, 0) ||
		*sampleRate < intermediateRate || math.Mod(*sampleRate, intermediateRate) != 0 {
		log.Fatal("sample-rate must be 1000000 Hz or a positive multiple of 12000 Hz")
	}
	decimation := int(*sampleRate / intermediateRate)
	// Preserve the +/-3 kHz channel, with a 1 kHz transition centered at
	// 3.5 kHz. Transition is normalized to input Fs; the constructor divides
	// cutoff by decimation, so cutoff is normalized to the decimator's output Fs.
	transitionBandwidth := 1000.0 / *sampleRate
	cutoff := 3500.0 / intermediateRate
	metrics := newMetrics(*mode)

	var onMessage func(time.Time, ft8.DecodedMessage)
	if !*noReport && *iqFile == "" {
		if *dialFrequency == 0 || *dialFrequency >= (1<<40)-6000 {
			log.Fatal("reporting requires --dial-frequency in Hz (or use --no-report)")
		}
		r, err := reporter.New(reporter.Config{
			Receiver: reporter.Receiver{Callsign: *receiverCall, Locator: *receiverGrid,
				Software: "go-ft8-reporter (go-ft8 v0.9.0)", Antenna: *antenna},
			Address: *reportAddress,
		})
		if err != nil {
			log.Fatal("initialize PSK Reporter: ", err)
		}
		metrics.reporter = r
		metrics.reportingEnabled.Set(1)
		go func() {
			for err := range r.Errors() {
				logger.Printf("PSK Reporter: %v", err)
			}
		}()
		defer func() {
			if err := r.Flush(); err != nil && !errors.Is(err, reporter.ErrRateLimited) {
				logger.Printf("PSK Reporter final flush: %v", err)
			} else if errors.Is(err, reporter.ErrRateLimited) {
				logger.Print("PSK Reporter: pending reports discarded at shutdown to respect send interval")
			}
			if err := r.Close(); err != nil {
				logger.Printf("close PSK Reporter: %v", err)
			}
		}()
		onMessage = func(slot time.Time, msg ft8.DecodedMessage) {
			spot, ok := spotFromMessage(*mode, slot, msg, *dialFrequency)
			if !ok {
				metrics.skipped.Inc()
				return
			}
			if _, err := r.Add(spot); err != nil {
				logger.Printf("PSK Reporter add %s: %v", spot.Callsign, err)
			}
		}
		logger.Printf("PSK Reporter enabled receiver=%s locator=%s dial=%dHz", *receiverCall, *receiverGrid, *dialFrequency)
	} else {
		logger.Print("PSK Reporter disabled")
	}
	closeMetrics, err := startMetrics(*metricsAddress, metrics, logger)
	if err != nil {
		log.Fatal("start metrics endpoint: ", err)
	}
	defer closeMetrics()

	var source interface {
		ReadIQ([]complex64) (int, error)
	}
	var sourceCloser io.Closer
	var captureStart time.Time
	if *iqFile != "" {
		var err error
		captureStart, err = time.Parse(time.RFC3339Nano, *startTime)
		if err != nil {
			log.Fatal("--iq-file requires --start-time in RFC3339 format: ", err)
		}
		file, err := os.Open(*iqFile)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		source = sdr.NewU8IQReader(file, blockSize)
		sourceCloser = file
	} else {
		if *startTime != "" {
			log.Fatal("--start-time requires --iq-file")
		}
		tcp, err := sdr.DialRTLTCP(*addr, blockSize)
		if err != nil {
			log.Fatal(err)
		}
		defer tcp.Close()
		source = tcp
		sourceCloser = tcp
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = sourceCloser.Close() }()

	shifter := sdr.NewFrequencyShifter(float32(*shiftRate))
	decimator, err := sdr.NewReceiverDecimator(
		decimation, float32(transitionBandwidth), float32(cutoff), sdr.WindowBlackman, blockSize)
	if err != nil {
		log.Fatal(err)
	}

	in := make([]complex64, blockSize)
	shifted := make([]complex64, blockSize)
	decimated := make([]complex64, blockSize)
	resampled := make([]complex64, blockSize)
	jobs := make(chan receiveSlot, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		decodeSlots(*mode, jobs, logger, onMessage, metrics)
	}()
	defer func() { close(jobs); <-done }()
	var collector *slotCollector
	period := 15 * time.Second
	if *mode == "ft4" {
		period = 7500 * time.Millisecond
	}

	for {
		n, err := source.ReadIQ(in)
		if n > 0 {
			metrics.iqSamples.Add(float64(n))
			if collector == nil {
				// Anchor the sample clock once; TCP delivery jitter must not
				// change the number or ordering of samples within a slot.
				first := captureStart
				if *iqFile == "" {
					first = time.Now().UTC().Add(-*streamLatency).Add(-time.Duration(float64(n) / *sampleRate * float64(time.Second)))
				}
				delay := float64(decimator.DelaySamples()) / *sampleRate
				if resampler != nil {
					delay += float64(resampler.DelaySamples()) / intermediateRate
				}
				first = first.Add(time.Duration(delay * float64(time.Second)))
				collector = newSlotCollector(first, period)
				logger.Printf("receiving mode=%s first_slot=%s", *mode, collector.start.Format(time.RFC3339Nano))
			}
			shiftedN := shifter.Process(in[:n], shifted[:n])
			decimatedN := decimator.Process(shifted[:shiftedN], decimated[:shiftedN])
			baseband := decimated[:decimatedN]
			if resampler != nil {
				count := resampler.Process(baseband, resampled)
				baseband = resampled[:count]
			}
			collector.push(baseband, func(slot receiveSlot) {
				select {
				case jobs <- slot:
				default:
					if *iqFile != "" {
						jobs <- slot
						return
					} // offline input can wait
					logger.Printf("dropping slot=%s: decoder queue full", slot.start.Format(time.RFC3339Nano))
					metrics.dropped.Inc()
				}
			})
		}
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return
			}
			logger.Printf("receiver stopped: %v", err)
			metrics.readErrors.Inc()
			return
		}
	}

}
