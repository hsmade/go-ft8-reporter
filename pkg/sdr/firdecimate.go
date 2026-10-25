package sdr

import (
	"fmt"
	"math"
)

type WindowType string

const (
	WindowBoxcar   WindowType = "boxcar"
	WindowBlackman WindowType = "blackman"
	WindowHamming  WindowType = "hamming"
)

type FIRDecimator struct {
	decimation int
	taps       []float32
	pending    []complex64
	scratch    []complex64
	pendingLen int
}

func NewFIRDecimator(decimation int, transition, cutoff float32, window WindowType) (*FIRDecimator, error) {
	if decimation <= 0 {
		return nil, fmt.Errorf("decimation must be > 0")
	}
	if transition <= 0 {
		return nil, fmt.Errorf("transition must be > 0")
	}

	if !isSupportedWindow(window) {
		return nil, fmt.Errorf("unsupported window %q", window)
	}

	taps := lowPassTaps(cutoff/float32(decimation), transition, window)
	return &FIRDecimator{
		decimation: decimation,
		taps:       taps,
		pending:    make([]complex64, len(taps)+decimation-1),
		scratch:    make([]complex64, len(taps)+decimation-1),
	}, nil
}

func (f *FIRDecimator) Process(in []complex64, out []complex64) int {
	total := f.pendingLen + len(in)
	if total < len(f.taps) {
		f.savePending(in, 0)
		return 0
	}

	maxOut := (total - len(f.taps)) / f.decimation
	if maxOut > len(out) {
		maxOut = len(out)
	}

	for i := 0; i < maxOut; i++ {
		start := i * f.decimation
		var accI float32
		var accQ float32
		for tapIndex, tap := range f.taps {
			sample := f.sampleAt(in, start+tapIndex)
			accI += real(sample) * tap
			accQ += imag(sample) * tap
		}
		out[i] = complex(accI, accQ)
	}

	f.savePending(in, maxOut*f.decimation)
	return maxOut
}

func (f *FIRDecimator) sampleAt(in []complex64, index int) complex64 {
	if index < f.pendingLen {
		return f.pending[index]
	}
	return in[index-f.pendingLen]
}

func (f *FIRDecimator) savePending(in []complex64, consumed int) {
	total := f.pendingLen + len(in)
	remaining := total - consumed
	for i := 0; i < remaining; i++ {
		f.scratch[i] = f.sampleAt(in, consumed+i)
	}
	copy(f.pending[:remaining], f.scratch[:remaining])
	f.pendingLen = remaining
}

func lowPassTaps(cutoff, transition float32, window WindowType) []float32 {
	length := filterLength(transition)
	taps := make([]float32, length)
	middle := length / 2
	taps[middle] = float32(2*math.Pi) * cutoff * windowKernel(window, 0)
	for i := 1; i <= middle; i++ {
		value := float32(math.Sin(float64(2*math.Pi*cutoff*float32(i)))/float64(i)) * windowKernel(window, float32(i)/float32(middle))
		taps[middle-i] = value
		taps[middle+i] = value
	}

	var sum float32
	for _, tap := range taps {
		sum += tap
	}
	for i := range taps {
		taps[i] /= sum
	}
	return taps
}

func filterLength(transition float32) int {
	result := int(4.0 / transition)
	if result%2 == 0 {
		result++
	}
	return result
}

func windowKernel(window WindowType, rate float32) float32 {
	r := 0.5 + rate/2
	switch window {
	case WindowBoxcar:
		return 1
	case WindowBlackman:
		return 0.42 - 0.5*float32(math.Cos(float64(2*math.Pi*r))) + 0.08*float32(math.Cos(float64(4*math.Pi*r)))
	case WindowHamming:
		return 0.54 - 0.46*float32(math.Cos(float64(2*math.Pi*r)))
	default:
		panic("unsupported window type")
	}
}

func isSupportedWindow(window WindowType) bool {
	switch window {
	case WindowBoxcar, WindowBlackman, WindowHamming:
		return true
	default:
		return false
	}
}
