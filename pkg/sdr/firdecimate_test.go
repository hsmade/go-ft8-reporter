package sdr

import "testing"

func TestLowPassTapsMatchExpectedLengthAndNormalization(t *testing.T) {
	taps := lowPassTaps(0.5/20, 0.05, WindowHamming)
	if len(taps) != 81 {
		t.Fatalf("expected 81 taps, got %d", len(taps))
	}

	var sum float32
	for _, tap := range taps {
		sum += tap
	}
	requireInDelta(t, 1.0, float64(sum), 1e-5)
}

func TestFIRDecimatorMatchesReferenceWhenSplitAcrossCalls(t *testing.T) {
	decimator, err := NewFIRDecimator(3, 0.2, 0.5, WindowHamming)
	if err != nil {
		t.Fatal(err)
	}
	refTaps := append([]float32(nil), decimator.taps...)

	in := []complex64{
		complex(1, 0.5), complex(2, -0.5), complex(3, 0.25), complex(4, -0.25),
		complex(5, 0.125), complex(6, -0.125), complex(7, 0.75), complex(8, -0.75),
		complex(9, 1), complex(10, -1), complex(11, 0.6), complex(12, -0.6),
		complex(13, 0.3), complex(14, -0.3), complex(15, 0.9), complex(16, -0.9),
		complex(17, 0.2), complex(18, -0.2), complex(19, 0.1), complex(20, -0.1),
		complex(21, 0.4), complex(22, -0.4), complex(23, 0.7), complex(24, -0.7),
		complex(25, 0.8), complex(26, -0.8), complex(27, 0.55), complex(28, -0.55),
		complex(29, 0.45), complex(30, -0.45),
	}

	want := referenceFIRDecimate(in, refTaps, 3)

	first := make([]complex64, len(want))
	second := make([]complex64, len(want))
	n1 := decimator.Process(in[:18], first)
	n2 := decimator.Process(in[18:], second)
	got := append(append([]complex64{}, first[:n1]...), second[:n2]...)

	if len(got) != len(want) {
		t.Fatalf("expected %d outputs, got %d", len(want), len(got))
	}

	for i := range want {
		requireInDelta(t, float64(real(want[i])), float64(real(got[i])), 1e-5)
		requireInDelta(t, float64(imag(want[i])), float64(imag(got[i])), 1e-5)
	}
}

func referenceFIRDecimate(in []complex64, taps []float32, decimation int) []complex64 {
	if len(in) < len(taps) {
		return nil
	}
	count := (len(in) - len(taps)) / decimation
	out := make([]complex64, count)
	for i := 0; i < count; i++ {
		start := i * decimation
		var accI float32
		var accQ float32
		for tapIndex, tap := range taps {
			accI += real(in[start+tapIndex]) * tap
			accQ += imag(in[start+tapIndex]) * tap
		}
		out[i] = complex(accI, accQ)
	}
	return out
}
