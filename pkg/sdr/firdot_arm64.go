//go:build arm64 && !purego

package sdr

// firDot computes a real-tap complex dot product using ARM64 NEON.
// len(in) must be at least len(taps). Vector accumulation changes rounding
// relative to the scalar implementation, but not the filter coefficients.
//
//go:noescape
func firDot(in []complex64, taps []float32) complex64
