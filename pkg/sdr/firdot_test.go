package sdr

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestFIRDotMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7, 8, 15, 16, 17, 81, 161, 717, 801} {
		in := make([]complex64, n)
		taps := make([]float32, n)
		var i, q float32
		for k := range in {
			in[k] = complex(rng.Float32()*2-1, rng.Float32()*2-1)
			taps[k] = rng.Float32()*2 - 1
			i += real(in[k]) * taps[k]
			q += imag(in[k]) * taps[k]
		}
		got := firDot(in, taps)
		for _, v := range [][2]float32{{real(got), i}, {imag(got), q}} {
			if math.Abs(float64(v[0]-v[1])) > 2e-5 {
				t.Fatalf("length %d: got %v, want %v", n, got, complex(i, q))
			}
		}
	}
}

func TestFIRStreamingRandomChunks(t *testing.T) {
	rng := rand.New(rand.NewSource(43))
	in := make([]complex64, 20000)
	for k := range in {
		in[k] = complex(rng.Float32()*2-1, rng.Float32()*2-1)
	}
	for _, factor := range []int{1, 5, 10, 20, 50} {
		f, err := NewFIRDecimator(factor, .005, .25, WindowHamming)
		if err != nil {
			t.Fatal(err)
		}
		want := referenceFIRDecimate(in, f.taps, factor)
		got := make([]complex64, 0, len(want))
		out := make([]complex64, len(in))
		for pos := 0; pos < len(in); {
			n := min(1+rng.Intn(1024), len(in)-pos)
			produced := f.Process(in[pos:pos+n], out)
			got = append(got, out[:produced]...)
			pos += n
		}
		if len(got) != len(want) {
			t.Fatalf("factor %d: count %d want %d", factor, len(got), len(want))
		}
		for k := range want {
			if math.Hypot(float64(real(got[k]-want[k])), float64(imag(got[k]-want[k]))) > 1e-5 {
				t.Fatalf("factor %d sample %d: got %v want %v", factor, k, got[k], want[k])
			}
		}
		if allocs := testing.AllocsPerRun(10, func() { f.Process(in[:4096], out) }); allocs != 0 {
			t.Fatalf("Process allocated %g times", allocs)
		}
	}
}

func BenchmarkFIRDecimator(b *testing.B) {
	for _, factor := range []int{10, 50} {
		b.Run(fmt.Sprintf("factor%d", factor), func(b *testing.B) {
			f, _ := NewFIRDecimator(factor, .005, .25, WindowHamming)
			in := make([]complex64, 16384)
			out := make([]complex64, len(in))
			for i := range in {
				in[i] = complex(float32(i%17)/17, float32(i%13)/13)
			}
			b.SetBytes(int64(len(in) * 8))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f.Process(in, out)
			}
		})
	}
}
