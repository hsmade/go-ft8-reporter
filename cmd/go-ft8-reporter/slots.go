package main

import (
	"log"
	"math"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft4"
	"github.com/ColonelBlimp/go-ft8/ft8"
)

const pcmRate = 12000

type receiveSlot struct {
	start time.Time
	pcm   []int16
}

type slotCollector struct {
	start  time.Time
	period time.Duration
	skip   int
	size   int
	pcm    []int16
}

func newSlotCollector(first time.Time, period time.Duration) *slotCollector {
	start := first.UTC().Truncate(period)
	if start.Before(first) {
		start = start.Add(period)
	}
	return &slotCollector{
		start: start, period: period,
		skip: int(math.Round(start.Sub(first).Seconds() * pcmRate)),
		size: int(period.Seconds() * pcmRate),
	}
}

// push consumes continuous, filtered IQ. Taking the real component produces
// receiver audio for positive-frequency (USB) signals; do not FM-demodulate
// FT8/FT4. Tune the shifter to the band's USB dial frequency.
func (c *slotCollector) push(iq []complex64, emit func(receiveSlot)) {
	if c.skip > 0 {
		n := min(c.skip, len(iq))
		c.skip -= n
		iq = iq[n:]
	}
	for _, sample := range iq {
		if c.pcm == nil {
			c.pcm = make([]int16, 0, c.size)
		}
		value := math.Round(float64(real(sample)) * 32768)
		value = max(-32768, min(32767, value))
		c.pcm = append(c.pcm, int16(value))
		if len(c.pcm) == c.size {
			emit(receiveSlot{start: c.start, pcm: c.pcm})
			c.start = c.start.Add(c.period)
			c.pcm = nil // ownership passes to the decoder
		}
	}
}

func decodeSlots(mode string, jobs <-chan receiveSlot, logger *log.Logger, onMessage func(time.Time, ft8.DecodedMessage), metrics *appMetrics) {
	// Keep hash and decode history across slots, confined to this worker.
	var decode func([]int16) ([]ft8.DecodedMessage, error)
	var candidates, attempts, failures int
	if mode == "ft4" {
		d := ft4.NewDecoder()
		decode = func(pcm []int16) ([]ft8.DecodedMessage, error) {
			r, err := d.DecodeMessagesChecked(pcm)
			candidates, attempts, failures = r.Diagnostics.CandidatesFound, r.Diagnostics.LDPCAttempts, r.Diagnostics.LDPCFailures
			return r.Messages, err
		}
	} else {
		d := ft8.NewDecoder()
		decode = func(pcm []int16) ([]ft8.DecodedMessage, error) {
			r, err := d.DecodeMessagesChecked(pcm)
			candidates, attempts, failures = r.Diagnostics.CandidatesFound, r.Diagnostics.LDPCAttempts, r.Diagnostics.LDPCFailures
			return r.Messages, err
		}
	}
	for slot := range jobs {
		started := time.Now()
		messages, err := decode(slot.pcm)
		metrics.observeDecode(messages, err, time.Since(started), candidates, attempts, failures)
		stamp := slot.start.Format(time.RFC3339Nano)
		if err != nil {
			logger.Printf("mode=%s slot=%s decode error: %v", mode, stamp, err)
			continue
		}
		logger.Printf("mode=%s slot=%s decoded=%d", mode, stamp, len(messages))
		for _, msg := range messages {
			logger.Printf("mode=%s slot=%s snr=%ddB freq=%.1fHz dt=%+.3fs status=%v text=%q payload=%v",
				mode, stamp, msg.SNR, msg.FreqHz, msg.DTSec, msg.ParseStatus, msg.Text, msg.Payload.Bits())
			if onMessage != nil {
				onMessage(slot.start, msg)
			}
		}
	}
}
