package sdr

import (
	"fmt"
	"math"
	"testing"
)

func TestFIRDecimatorReceiverRates(t *testing.T) {
	for _, rate := range []int{960000, 2400000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			d, err := NewFIRDecimator(rate/48000, float32(1340.0/float64(rate)), 0.2325, WindowBlackman)
			if err != nil {
				t.Fatal(err)
			}
			in := make([]complex64, len(d.taps)*3+321)
			for i := range in {
				in[i] = complex(float32(math.Sin(float64(i)*0.17)), float32(math.Cos(float64(i)*0.31)))
			}
			want := referenceFIRDecimate(in, d.taps, d.decimation)
			var got []complex64
			out := make([]complex64, len(in))
			for start := 0; start < len(in); {
				end := min(start+997, len(in))
				n := d.Process(in[start:end], out)
				got = append(got, out[:n]...)
				start = end
			}
			if len(got) != len(want) {
				t.Fatalf("outputs %d want %d", len(got), len(want))
			}
			for i := range want {
				requireInDelta(t, float64(real(want[i])), float64(real(got[i])), 1e-6)
				requireInDelta(t, float64(imag(want[i])), float64(imag(got[i])), 1e-6)
			}
		})
	}
}

func BenchmarkFIRDecimatorReceiverRates(b *testing.B) {
	for _, rate := range []int{960000, 2400000} {
		b.Run(fmt.Sprint(rate), func(b *testing.B) {
			d, err := NewFIRDecimator(rate/48000, float32(1340.0/float64(rate)), 0.2325, WindowBlackman)
			if err != nil {
				b.Fatal(err)
			}
			in := make([]complex64, rate/10)
			out := make([]complex64, len(in))
			b.SetBytes(int64(len(in) * 8))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.Process(in, out)
			}
		})
	}
}
