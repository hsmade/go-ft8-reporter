package sdr

import (
	"fmt"
	"math"
	"math/cmplx"
	"testing"
)

func response(taps []float32, frequency float64) float64 {
	var sum complex128
	for i, v := range taps {
		sum += complex(float64(v), 0) * cmplx.Exp(complex(0, -2*math.Pi*frequency*float64(i)))
	}
	return cmplx.Abs(sum)
}

func TestReceiverDecimatorResponse(t *testing.T) {
	for _, rate := range []int{960000, 2400000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			decimation := rate / 48000
			d, err := NewReceiverDecimator(decimation, float32(1340.0/float64(rate)), 0.2325, WindowBlackman, rate/10)
			if err != nil {
				t.Fatal(err)
			}
			if d.first == nil {
				t.Fatal("expected staged decimation")
			}
			original, _ := NewFIRDecimator(decimation, float32(1340.0/float64(rate)), 0.2325, WindowBlackman)
			gain := func(hz float64) float64 {
				return response(d.first.taps, hz/float64(rate)) * response(d.channel.taps, hz/float64(rate/d.first.decimation))
			}
			// Preserve channel response, including bandwidth used for the SNR path.
			for hz := 0.; hz <= 12500; hz += 500 {
				want := response(original.taps, hz/float64(rate))
				if math.Abs(gain(hz)-want) > 0.001 {
					t.Fatalf("passband changed at %.0f Hz: %g vs %g", hz, gain(hz), want)
				}
			}
			// Sweep blockers, including bands that alias into the channel after stage 1.
			for hz := 13000.; hz <= float64(rate)/2; hz += 500 {
				if g := gain(hz); g > 0.001 {
					t.Fatalf("insufficient rejection at %.0f Hz: %g", hz, g)
				}
			}
		})
	}
}

func TestReceiverDecimatorStreaming(t *testing.T) {
	for _, rate := range []int{960000, 2400000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			size := rate / 10
			makeD := func() *ReceiverDecimator {
				d, e := NewReceiverDecimator(rate/48000, float32(1340.0/float64(rate)), 0.2325, WindowBlackman, size)
				if e != nil {
					t.Fatal(e)
				}
				return d
			}
			input := make([]complex64, size)
			for i := range input {
				input[i] = complex(float32(math.Sin(float64(i)*0.03)), float32(math.Cos(float64(i)*0.02)))
			}
			whole, split := makeD(), makeD()
			out := make([]complex64, size)
			n := whole.Process(input, out)
			want := append([]complex64(nil), out[:n]...)
			var got []complex64
			for start := 0; start < size; {
				end := min(size, start+997)
				n := split.Process(input[start:end], out)
				got = append(got, out[:n]...)
				start = end
			}
			if len(got) != len(want) {
				t.Fatalf("count %d want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("chunk-dependent output at %d", i)
				}
			}
			// Once primed, every 100ms block yields exactly 4800 samples.
			if n := whole.Process(input, out); n != 4800 {
				t.Fatalf("steady rate: got %d", n)
			}
			if a := testing.AllocsPerRun(3, func() { whole.Process(input, out) }); a != 0 {
				t.Fatalf("allocations: %g", a)
			}
		})
	}
}

func TestReceiverDecimatorFallback(t *testing.T) {
	d, err := NewReceiverDecimator(3, 0.02, 0.25, WindowHamming, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if d.first != nil {
		t.Fatal("unexpected coarse stage")
	}
	if _, err := NewReceiverDecimator(20, 0.001, 0.25, WindowBlackman, 0); err == nil {
		t.Fatal("accepted zero block size")
	}
}

func BenchmarkReceiverDecimator(b *testing.B) {
	for _, rate := range []int{960000, 2400000} {
		b.Run(fmt.Sprint(rate), func(b *testing.B) {
			size := rate / 10
			d, err := NewReceiverDecimator(rate/48000, float32(1340.0/float64(rate)), 0.2325, WindowBlackman, size)
			if err != nil {
				b.Fatal(err)
			}
			in, out := make([]complex64, size), make([]complex64, size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.Process(in, out)
			}
		})
	}
}

// Exercise the actual CLI configuration, including coarse-stage aliases.
func TestReceiverDecimator12k(t *testing.T) {
	for _, rate := range []int{960000, 2400000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			const size = 4096
			makeD := func() *ReceiverDecimator {
				d, err := NewReceiverDecimator(rate/12000, float32(1000.0/float64(rate)), float32(3500.0/12000), WindowBlackman, size)
				if err != nil {
					t.Fatal(err)
				}
				return d
			}
			d := makeD()
			if d.first == nil {
				t.Fatal("live 12 kHz path must use staged decimation")
			}
			original, _ := NewFIRDecimator(rate/12000, float32(1000.0/float64(rate)), float32(3500.0/12000), WindowBlackman)
			gain := func(hz float64) float64 {
				return response(d.first.taps, hz/float64(rate)) * response(d.channel.taps, hz/float64(rate/d.first.decimation))
			}
			for hz := 0.; hz <= 4000; hz += 100 {
				if math.Abs(gain(hz)-response(original.taps, hz/float64(rate))) > 0.001 {
					t.Fatalf("channel response changed at %g Hz: staged=%g original=%g", hz, gain(hz), response(original.taps, hz/float64(rate)))
				}
			}
			for hz := 4500.; hz <= float64(rate)/2; hz += 250 {
				if g := gain(hz); g > 0.001 {
					t.Fatalf("blocker rejection at %g Hz: %g", hz, g)
				}
			}
			wantDelay := (len(d.first.taps)-1)/2 + d.first.decimation*((len(d.channel.taps)-1)/2)
			if d.DelaySamples() != wantDelay {
				t.Fatal("incorrect timestamp delay")
			}
			input := make([]complex64, size)
			for i := range input {
				input[i] = complex(float32(math.Sin(float64(i)*0.03)), float32(math.Cos(float64(i)*0.02)))
			}
			whole, split := makeD(), makeD()
			out := make([]complex64, size)
			var want, got []complex64
			for block := 0; block < 20; block++ {
				n := whole.Process(input, out)
				want = append(want, out[:n]...)
				for start := 0; start < size; {
					end := min(size, start+997)
					n = split.Process(input[start:end], out)
					got = append(got, out[:n]...)
					start = end
				}
			}
			if len(want) != len(got) {
				t.Fatalf("sample counts %d != %d", len(want), len(got))
			}
			for i := range want {
				if want[i] != got[i] {
					t.Fatalf("chunk-dependent output at %d", i)
				}
			}
			if a := testing.AllocsPerRun(3, func() { whole.Process(input, out) }); a != 0 {
				t.Fatalf("allocations: %g", a)
			}
		})
	}
}

func BenchmarkReceiver12kPipeline(b *testing.B) {
	for _, staged := range []bool{false, true} {
		b.Run(fmt.Sprintf("staged=%t", staged), func(b *testing.B) {
			const rate, size = 2400000, 4096
			d, err := NewReceiverDecimator(rate/12000, float32(1000.0/rate), float32(3500.0/12000), WindowBlackman, size)
			if err != nil {
				b.Fatal(err)
			}
			if !staged {
				d.channel, _ = NewFIRDecimator(rate/12000, float32(1000.0/rate), float32(3500.0/12000), WindowBlackman)
				d.first = nil
			}
			in, out := make([]complex64, size), make([]complex64, size)
			shifter := NewFrequencyShifter(0.21916667)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				shifter.Process(in, in)
				d.Process(in, out)
			}
		})
	}
}
