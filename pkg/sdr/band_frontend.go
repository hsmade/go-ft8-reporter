package sdr

import (
	"fmt"
	"math"
)

// BandFrontend reads one wideband stream and shares its mixer/coarse FIR across
// channels. Each channel retains its own narrow FIR and continuous phase state.
// Process returns borrowed channel buffers valid until its next call.
type BandFrontend struct {
	mixer                  *FrequencyShifter
	coarse                 *FIRDecimator
	channels               []*FIRDecimator
	mixers                 []*FrequencyShifter
	shifted, intermediate  []complex64
	channelShifted, output [][]complex64
	factor, blockSize      int
}

func NewBandFrontend(rate int, center float64, dials []float64, blockSize int) (*BandFrontend, error) {
	if rate < 120000 || rate%120000 != 0 || blockSize <= 0 || len(dials) == 0 || !finite(center) {
		return nil, fmt.Errorf("shared frontend requires a sample rate divisible by 120000, positive block size and channel frequencies")
	}
	low, high := dials[0], dials[0]
	for _, dial := range dials {
		if !finite(dial) || dial <= 0 {
			return nil, fmt.Errorf("invalid channel frequency")
		}
		low = math.Min(low, dial)
		high = math.Max(high, dial)
	}
	sharedCenter := (low + high) / 2
	const factor = 10
	intermediateRate := rate / factor
	for _, dial := range dials {
		if math.Abs(dial-center)+4000 >= float64(rate)/2 || math.Abs(dial-sharedCenter)+4000 >= float64(intermediateRate)/8 {
			return nil, fmt.Errorf("channel %.0f is outside the shared frontend passband", dial)
		}
	}
	coarse, err := NewFIRDecimator(factor, 0.25/factor, 0.25, WindowBlackman)
	if err != nil {
		return nil, err
	}
	f := &BandFrontend{mixer: NewFrequencyShifter(float32((center - sharedCenter) / float64(rate))), coarse: coarse, factor: factor, blockSize: blockSize, shifted: make([]complex64, blockSize), intermediate: make([]complex64, blockSize)}
	for _, dial := range dials {
		channel, err := NewFIRDecimator(intermediateRate/12000, float32(1000.0/float64(intermediateRate)), float32(3500.0/12000), WindowBlackman)
		if err != nil {
			return nil, err
		}
		f.channels = append(f.channels, channel)
		f.mixers = append(f.mixers, NewFrequencyShifter(float32((sharedCenter-dial)/float64(intermediateRate))))
		f.channelShifted = append(f.channelShifted, make([]complex64, blockSize))
		f.output = append(f.output, make([]complex64, blockSize))
	}
	return f, nil
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func (f *BandFrontend) DelaySamples(channel int) int {
	return (len(f.coarse.taps)-1)/2 + f.factor*((len(f.channels[channel].taps)-1)/2)
}
func (f *BandFrontend) Process(in []complex64, result [][]complex64) {
	if len(in) > f.blockSize || len(result) != len(f.channels) {
		panic("shared frontend: invalid buffer sizes")
	}
	n := f.mixer.Process(in, f.shifted)
	n = f.coarse.Process(f.shifted[:n], f.intermediate)
	for i, channel := range f.channels {
		f.mixers[i].Process(f.intermediate[:n], f.channelShifted[i])
		count := channel.Process(f.channelShifted[i][:n], f.output[i])
		result[i] = f.output[i][:count]
	}
}
