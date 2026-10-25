package sdr

import (
	"math"
	"testing"
)

func TestRationalResamplerStreaming(t *testing.T) {
	input := make([]complex64, 25000)
	for i := range input {
		phase := 2 * math.Pi * 1000 * float64(i) / 25000
		input[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
	}
	whole, _ := NewRationalResampler(12, 25)
	split, _ := NewRationalResampler(12, 25)
	out := make([]complex64, len(input))
	n := whole.Process(input, out)
	want := append([]complex64(nil), out[:n]...)
	var got []complex64
	for start := 0; start < len(input); {
		end := min(start+37, len(input))
		n := split.Process(input[start:end], out)
		got = append(got, out[:n]...)
		start = end
	}
	if len(got) != len(want) {
		t.Fatalf("chunk-dependent count %d vs %d", len(got), len(want))
	}
	for i, sample := range got {
		if sample != want[i] {
			t.Fatalf("chunk-dependent sample %d", i)
		}
		position := float64(resampleHalf) + float64(i)*25/12
		phase := 2 * math.Pi * 1000 * position / 25000
		if math.Abs(float64(real(sample))-math.Cos(phase)) > 0.001 || math.Abs(float64(imag(sample))-math.Sin(phase)) > 0.001 {
			t.Fatalf("incorrect phase/gain at sample %d: %v", i, sample)
		}
	}
	if n := whole.Process(input, out); n != 12000 {
		t.Fatalf("steady rate = %d, want 12000", n)
	}
}

func TestRationalResamplerRejectsAliases(t *testing.T) {
	r, _ := NewRationalResampler(12, 25)
	input, output := make([]complex64, 25000), make([]complex64, 25000)
	for i := range input {
		phase := 2 * math.Pi * 9000 * float64(i) / 25000
		input[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
	}
	n := r.Process(input, output)
	for _, sample := range output[:n] {
		if math.Hypot(float64(real(sample)), float64(imag(sample))) > 0.001 {
			t.Fatal("alias rejection below 60dB")
		}
	}
	for _, factors := range [][2]int{{0, 25}, {25, 12}, {1025, 2048}} {
		if _, err := NewRationalResampler(factors[0], factors[1]); err == nil {
			t.Fatalf("accepted %v", factors)
		}
	}
}
