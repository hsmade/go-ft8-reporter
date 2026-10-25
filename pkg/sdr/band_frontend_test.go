package sdr

import (
	"math"
	"testing"
)

func TestBandFrontendStreaming(t *testing.T) {
	for _, rate := range []int{960000, 2400000} {
		makeF := func() *BandFrontend {
			f, e := NewBandFrontend(rate, 144400000, []float64{144174000, 144170000}, 4096)
			if e != nil {
				t.Fatal(e)
			}
			return f
		}
		whole, split := makeF(), makeF()
		a, b := make([][]complex64, 2), make([][]complex64, 2)
		want, got := make([][]complex64, 2), make([][]complex64, 2)
		in := make([]complex64, 4096)
		index := 0
		for block := 0; block < 20; block++ {
			for i := range in {
				phase := 2 * math.Pi * (-225000) * float64(index) / float64(rate)
				in[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
				index++
			}
			whole.Process(in, a)
			for c := range a {
				want[c] = append(want[c], a[c]...)
			}
			for start := 0; start < len(in); {
				end := min(start+997, len(in))
				split.Process(in[start:end], b)
				for c := range b {
					got[c] = append(got[c], b[c]...)
				}
				start = end
			}
		}
		for c := range want {
			if len(want[c]) != len(got[c]) {
				t.Fatal("chunk-dependent sample count")
			}
			for i := range want[c] {
				if want[c][i] != got[c][i] { // mixer phase has float32 chunk-rounding noise
					if math.Hypot(float64(real(want[c][i]-got[c][i])), float64(imag(want[c][i]-got[c][i]))) > 0.02 {
						t.Fatalf("chunk discontinuity channel %d sample %d", c, i)
					}
				}
			}
		}
		if a := testing.AllocsPerRun(3, func() { whole.Process(in, a) }); a != 0 {
			t.Fatalf("allocations %g", a)
		}
	}
}
func TestBandFrontendCoverage(t *testing.T) {
	for _, dials := range [][]float64{{144174000, 144170000}, {432174000, 432170000}} {
		rate := 2400000
		center := dials[0] + 526000
		f, e := NewBandFrontend(rate, center, dials, 4096)
		if e != nil {
			t.Fatal(e)
		}
		coarseRate := rate / f.factor
		mid := (dials[0] + dials[1]) / 2
		for c, dial := range dials {
			for hz := -float64(rate) / 2; hz < float64(rate)/2; hz += 500 {
				offset := dial - mid + hz
				gain := response(f.coarse.taps, offset/float64(rate)) * response(f.channels[c].taps, hz/float64(coarseRate))
				if math.Abs(hz) <= 3000 && math.Abs(gain-response(f.channels[c].taps, hz/float64(coarseRate))) > 0.001 {
					t.Fatalf("passband %gHz gain %g", hz, gain)
				}
				if math.Abs(hz) >= 4500 && gain > 0.001 {
					t.Fatalf("alias/blocker %gHz gain %g", hz, gain)
				}
			}
		}
	}
	for _, rate := range []int{1000000, 0, -1} {
		if _, e := NewBandFrontend(rate, 144400000, []float64{144174000}, 4096); e == nil {
			t.Fatal("invalid sample rate accepted")
		}
	}
	if _, e := NewBandFrontend(2400000, 144775000, []float64{144170000, 145170000}, 4096); e == nil {
		t.Fatal("out-of-passband channel accepted")
	}
}
func BenchmarkBandFrontend(b *testing.B) {
	for _, shared := range []bool{false, true} {
		b.Run(map[bool]string{false: "independent", true: "shared"}[shared], func(b *testing.B) {
			const rate, size = 2400000, 4096
			dials := []float64{144174000, 144170000}
			f, e := NewBandFrontend(rate, 144400000, dials, size)
			if e != nil {
				b.Fatal(e)
			}
			in, out := make([]complex64, size), make([]complex64, size)
			outputs := make([][]complex64, 2)
			mixers := []*FrequencyShifter{NewFrequencyShifter(float32((144775000 - dials[0]) / rate)), NewFrequencyShifter(float32((144775000 - dials[1]) / rate))}
			filters := make([]*ReceiverDecimator, 2)
			for i := range filters {
				filters[i], _ = NewReceiverDecimator(200, float32(1000.0/rate), float32(3500.0/12000), WindowBlackman, size)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if shared {
					f.Process(in, outputs)
				} else {
					for c := range mixers {
						mixers[c].Process(in, out)
						filters[c].Process(out, in)
					}
				}
			}
		})
	}
}
