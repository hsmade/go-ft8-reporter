package main

import (
	"bytes"
	"log"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ColonelBlimp/go-ft8/ft4"
	"github.com/ColonelBlimp/go-ft8/ft8"
	"github.com/hsmade/go-ft8-reporter/pkg/reporter"
)

func TestSlotCollectorBoundaries(t *testing.T) {
	for _, period := range []time.Duration{15 * time.Second, 7500 * time.Millisecond} {
		t.Run(period.String(), func(t *testing.T) {
			// Start 250ms before the minute rolls over. Chunk boundaries
			// deliberately cross both the first slot and later slot edges.
			boundary := time.Date(2026, 10, 25, 12, 1, 0, 0, time.UTC)
			c := newSlotCollector(boundary.Add(-250*time.Millisecond), period)
			input := make([]complex64, 3000+2*c.size+17)
			for i := range input {
				input[i] = complex(float32(i%100)/128, 1)
			}
			var slots []receiveSlot
			for offset := 0; offset < len(input); {
				end := min(offset+997, len(input))
				c.push(input[offset:end], func(s receiveSlot) { slots = append(slots, s) })
				offset = end
			}
			if len(slots) != 2 || len(c.pcm) != 17 {
				t.Fatalf("got %d slots and %d trailing samples", len(slots), len(c.pcm))
			}
			for j, s := range slots {
				if !s.start.Equal(boundary.Add(time.Duration(j)*period)) || len(s.pcm) != c.size {
					t.Fatalf("wrong slot timestamp or length: %s, %d", s.start, len(s.pcm))
				}
				for i, v := range s.pcm {
					want := int16(((3000 + j*c.size + i) % 100) * 256)
					if v != want {
						t.Fatalf("slot %d sample %d: got %d want %d", j, i, v, want)
					}
				}
			}
		})
	}
}

func TestPCMConversion(t *testing.T) {
	c := newSlotCollector(time.Unix(0, 0), time.Second)
	c.push([]complex64{2 + 3i, -2 - 3i, 0.5 + 1i, -0.5 - 1i}, func(receiveSlot) { t.Fatal("partial slot emitted") })
	for i, want := range []int16{32767, -32768, 16384, -16384} {
		if c.pcm[i] != want {
			t.Fatalf("sample %d: got %d want %d", i, c.pcm[i], want)
		}
	}
}

func TestDecodeSlotsSilence(t *testing.T) {
	for _, mode := range []string{"ft8", "ft4"} {
		size := 180000
		if mode == "ft4" {
			size = ft4.SlotSamples
		}
		jobs := make(chan receiveSlot, 1)
		jobs <- receiveSlot{start: time.Unix(0, 0), pcm: make([]int16, size)}
		close(jobs)
		var output bytes.Buffer
		decodeSlots(mode, jobs, log.New(&output, "", 0), nil, nil)
		if !strings.Contains(output.String(), "decoded=0") || strings.Contains(output.String(), "error") {
			t.Fatalf("%s: %s", mode, output.String())
		}
	}
}

func TestFT4IQToLoggedMessage(t *testing.T) {
	encoded, err := ft4.EncodeStandardMessage("CQ 7Q5MLV KH67")
	if err != nil {
		t.Fatal(err)
	}
	// An independent continuous-phase 4-FSK signal exercises IQ -> PCM ->
	// slot assembly -> real decoder -> logger without network or fixtures.
	iq := make([]complex64, ft4.SlotSamples)
	phase := 0.0
	for symbol, tone := range encoded.Tones {
		frequency := 1500 + float64(tone)*ft4.ToneSpacingHz
		for j := 0; j < ft4.SamplesPerSymbol; j++ {
			i := pcmRate/2 + symbol*ft4.SamplesPerSymbol + j
			iq[i] = complex(float32(0.5*math.Cos(phase)), float32(0.5*math.Sin(phase)))
			phase += 2 * math.Pi * frequency / pcmRate
		}
	}
	c := newSlotCollector(time.Unix(0, 0), 7500*time.Millisecond)
	jobs := make(chan receiveSlot, 1)
	c.push(iq, func(s receiveSlot) { jobs <- s })
	close(jobs)
	var output bytes.Buffer
	var reported []reporter.Spot
	m := newMetrics("ft4")
	decodeSlots("ft4", jobs, log.New(&output, "", 0), func(slot time.Time, msg ft8.DecodedMessage) {
		spot, ok := spotFromMessage("ft4", slot, msg, 432170000)
		if ok {
			reported = append(reported, spot)
		}
	}, m)
	if !strings.Contains(output.String(), `text="CQ 7Q5MLV KH67"`) {
		t.Fatalf("message not recovered: %s", output.String())
	}
	if len(reported) != 1 || reported[0].Callsign != "7Q5MLV" || reported[0].Locator != "KH67" || reported[0].Mode != "FT4" || math.Abs(float64(reported[0].FrequencyHz)-432171500) > 3 {
		t.Fatalf("incorrect decoded report: %+v", reported)
	}
	if metricValue(t, m, "decoded_messages_total", "status", "parsed") != 1 || metricValue(t, m, "decode_slots_total", "result", "success") != 1 {
		t.Fatal("successful decode metrics were not updated")
	}
}
