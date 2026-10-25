package sdr

import "fmt"

// ReceiverDecimator reduces wideband IQ before applying the narrow channel
// filter. Its Process method allocates nothing. Callers must provide output
// space for all input samples (a conservative bound) and blocks <= blockSize.
type ReceiverDecimator struct {
	first, channel *FIRDecimator
	intermediate   []complex64
	blockSize      int
}

// DelaySamples is the linear-phase filter group delay in input samples.
// The first output sample represents this position in the input stream.
func (d *ReceiverDecimator) DelaySamples() int {
	delay := (len(d.channel.taps) - 1) / 2
	if d.first != nil {
		delay = (len(d.first.taps)-1)/2 + d.first.decimation*delay
	}
	return delay
}

func NewReceiverDecimator(decimation int, transition, cutoff float32, window WindowType, blockSize int) (*ReceiverDecimator, error) {
	if blockSize <= 0 {
		return nil, fmt.Errorf("block size must be > 0")
	}
	// Keep the existing 48 kHz paths and stage the live 12 kHz paths too.
	// 960k -> 96k -> 12k; 2.4M -> 240k -> 12k.
	factor := 1
	switch decimation {
	case 20:
		factor = 5
	case 50, 80, 200:
		factor = 10
	}
	// The coarse filter passband ends at intermediate Fs/8. Fall back for
	// unusually wide channel configurations so their response is preserved.
	if cutoff/float32(decimation)+transition/2 >= 0.125/float32(factor) {
		factor = 1
	}
	channel, err := NewFIRDecimator(decimation/factor, transition*float32(factor), cutoff, window)
	if err != nil {
		return nil, err
	}
	d := &ReceiverDecimator{channel: channel, blockSize: blockSize}
	if factor > 1 {
		// Transition spans Fs_intermediate/8 .. 3*Fs_intermediate/8;
		// rejection is established before the first stage's Nyquist boundary.
		d.first, err = NewFIRDecimator(factor, 0.25/float32(factor), 0.25, WindowBlackman)
		if err != nil {
			return nil, err
		}
		d.intermediate = make([]complex64, blockSize)
	}
	return d, nil
}

func (d *ReceiverDecimator) Process(in, out []complex64) int {
	if len(in) > d.blockSize || len(out) < len(in) {
		panic("receiver decimator: invalid buffer sizes")
	}
	if len(in) == 0 {
		return 0
	}
	if d.first == nil {
		return d.channel.Process(in, out)
	}
	n := d.first.Process(in, d.intermediate)
	if n == 0 {
		return 0
	}
	return d.channel.Process(d.intermediate[:n], out)
}
