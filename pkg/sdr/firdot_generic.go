//go:build !arm64 || purego

package sdr

func firDot(in []complex64, taps []float32) complex64 {
	var i, q float32
	for n, tap := range taps {
		i += real(in[n]) * tap
		q += imag(in[n]) * tap
	}
	return complex(i, q)
}
