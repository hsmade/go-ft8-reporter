package sdr

import "math"

const twoPi = 2 * math.Pi

type IQBlock interface {
	Process(in []complex64, out []complex64) int
}

// FrequencyShifter mixes complex baseband by a rate relative to sample rate.
// A rate of 0.25 shifts by Fs/4.
type FrequencyShifter struct {
	rate           float32
	startingPhase  float64
	phaseIncrement float64
	dsin           [4]float32
	dcos           [4]float32
}

func NewFrequencyShifter(rate float32) *FrequencyShifter {
	s := &FrequencyShifter{}
	s.SetRate(rate)
	return s
}

func (s *FrequencyShifter) SetRate(rate float32) {
	s.rate = rate
	s.phaseIncrement = twoPi * float64(rate)
	for i := 0; i < len(s.dsin); i++ {
		phase := s.phaseIncrement * float64(i+1)
		s.dsin[i] = float32(math.Sin(float64(phase)))
		s.dcos[i] = float32(math.Cos(float64(phase)))
	}
}

func (s *FrequencyShifter) Process(in []complex64, out []complex64) int {
	n := len(in)
	if n == 0 {
		return 0
	}
	if len(out) < n {
		n = len(out)
	}
	if n == 0 {
		return 0
	}

	cosStart := float32(math.Cos(float64(s.startingPhase)))
	sinStart := float32(math.Sin(float64(s.startingPhase)))

	i := 0
	for ; i+3 < n; i += 4 {
		cos0 := cosStart*s.dcos[0] - sinStart*s.dsin[0]
		sin0 := sinStart*s.dcos[0] + cosStart*s.dsin[0]
		out[i] = mix(in[i], cos0, sin0)

		cos1 := cosStart*s.dcos[1] - sinStart*s.dsin[1]
		sin1 := sinStart*s.dcos[1] + cosStart*s.dsin[1]
		out[i+1] = mix(in[i+1], cos1, sin1)

		cos2 := cosStart*s.dcos[2] - sinStart*s.dsin[2]
		sin2 := sinStart*s.dcos[2] + cosStart*s.dsin[2]
		out[i+2] = mix(in[i+2], cos2, sin2)

		cos3 := cosStart*s.dcos[3] - sinStart*s.dsin[3]
		sin3 := sinStart*s.dcos[3] + cosStart*s.dsin[3]
		out[i+3] = mix(in[i+3], cos3, sin3)

		cosStart = cos3
		sinStart = sin3
	}

	stepCos := s.dcos[0]
	stepSin := s.dsin[0]
	for ; i < n; i++ {
		cosNext := cosStart*stepCos - sinStart*stepSin
		sinNext := sinStart*stepCos + cosStart*stepSin
		out[i] = mix(in[i], cosNext, sinNext)
		cosStart = cosNext
		sinStart = sinNext
	}

	s.startingPhase = math.Remainder(s.startingPhase+float64(n)*s.phaseIncrement, twoPi)

	return n
}

func mix(in complex64, cosv float32, sinv float32) complex64 {
	i := real(in)
	q := imag(in)
	return complex(cosv*i-sinv*q, sinv*i+cosv*q)
}
