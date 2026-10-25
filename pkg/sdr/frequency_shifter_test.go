package sdr

import (
	"math"
	"testing"
)

func TestFrequencyShifterMatchesReferenceMixer(t *testing.T) {
	shifter := NewFrequencyShifter(0.125)
	in := []complex64{
		complex(1, 0),
		complex(0.5, -0.25),
		complex(-0.25, 0.75),
		complex(-1, 0.5),
		complex(0.125, -0.875),
		complex(0.75, 0.25),
	}
	out := make([]complex64, len(in))

	n := shifter.Process(in, out)

	if n != len(in) {
		t.Fatalf("expected %d samples, got %d", len(in), n)
	}
	for i := range out {
		want := referenceShift(in[i], 0.125, i)
		requireInDelta(t, float64(real(want)), float64(real(out[i])), 1e-5)
		requireInDelta(t, float64(imag(want)), float64(imag(out[i])), 1e-5)
	}
}

func TestFrequencyShifterMaintainsStateAcrossCalls(t *testing.T) {
	rate := float32(-0.2)
	input := []complex64{
		complex(1, 0),
		complex(0.25, 0.5),
		complex(-0.5, 0.75),
		complex(0.125, -0.25),
		complex(-1, -1),
		complex(0.75, 0.25),
		complex(-0.125, 0.625),
	}

	wholeShifter := NewFrequencyShifter(rate)
	wholeOut := make([]complex64, len(input))
	wholeShifter.Process(input, wholeOut)

	splitShifter := NewFrequencyShifter(rate)
	splitOut := make([]complex64, len(input))
	first := splitShifter.Process(input[:3], splitOut[:3])
	second := splitShifter.Process(input[3:], splitOut[3:])

	if first != 3 {
		t.Fatalf("expected first call to produce 3 samples, got %d", first)
	}
	if second != 4 {
		t.Fatalf("expected second call to produce 4 samples, got %d", second)
	}
	for i := range wholeOut {
		requireInDelta(t, float64(real(wholeOut[i])), float64(real(splitOut[i])), 1e-5)
		requireInDelta(t, float64(imag(wholeOut[i])), float64(imag(splitOut[i])), 1e-5)
	}
}

func referenceShift(sample complex64, rate float64, index int) complex64 {
	phase := 2 * math.Pi * rate * float64(index+1)
	cosv := float32(math.Cos(phase))
	sinv := float32(math.Sin(phase))
	return complex(
		cosv*real(sample)-sinv*imag(sample),
		sinv*real(sample)+cosv*imag(sample),
	)
}

func requireInDelta(t *testing.T, want, got, delta float64) {
	t.Helper()
	if math.Abs(want-got) > delta {
		t.Fatalf("expected %f, got %f (delta %f)", want, got, delta)
	}
}
