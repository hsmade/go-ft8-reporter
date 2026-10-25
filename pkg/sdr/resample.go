package sdr

import (
	"fmt"
	"math"
)

const resampleHalf = 24

// RationalResampler performs band-limited rational downsampling of IQ.
// Process is continuous across blocks; out must have room for len(in) samples.
type RationalResampler struct {
	up, down    int64
	next, index int64
	ring        [2*resampleHalf + 1]complex64
	taps        [][]float32
}

func NewRationalResampler(up, down int) (*RationalResampler, error) {
	if up <= 0 || down < up || up > 1024 {
		return nil, fmt.Errorf("resampler requires 0 < up <= down and up <= 1024")
	}
	r := &RationalResampler{up: int64(up), down: int64(down), next: int64(resampleHalf * up), index: -1}
	cutoff := 0.45 * float64(up) / float64(down)
	for phase := 0; phase < up; phase++ {
		taps := make([]float32, len(r.ring))
		sum := 0.0
		for j := range taps {
			x := float64(j-resampleHalf) - float64(phase)/float64(up)
			v := 2 * cutoff
			if x != 0 {
				v = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
			}
			w := 0.42 + 0.5*math.Cos(math.Pi*x/(resampleHalf+1)) + 0.08*math.Cos(2*math.Pi*x/(resampleHalf+1))
			taps[j] = float32(v * w)
			sum += v * w
		}
		for j := range taps {
			taps[j] /= float32(sum)
		}
		r.taps = append(r.taps, taps)
	}
	return r, nil
}

// DelaySamples is the first output's center position in input samples.
func (r *RationalResampler) DelaySamples() int { return resampleHalf }

func (r *RationalResampler) Process(in, out []complex64) int {
	if len(out) < len(in) {
		panic("resampler: output buffer too short")
	}
	n := 0
	for _, sample := range in {
		r.index++
		r.ring[r.index%int64(len(r.ring))] = sample
		center := r.next / r.up
		if center+resampleHalf > r.index {
			continue
		}
		var value complex64
		for j, tap := range r.taps[r.next%r.up] {
			i := (center - resampleHalf + int64(j)) % int64(len(r.ring))
			value += r.ring[i] * complex(tap, 0)
		}
		out[n] = value
		n++
		r.next += r.down
	}
	return n
}
