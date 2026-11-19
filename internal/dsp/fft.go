// Package dsp holds signal-processing helpers shared by the audio tools.
package dsp

import (
	"math"
	"math/cmplx"
)

// FFT is a radix-2 transform of a fixed power-of-two size.
type FFT struct {
	n   int
	rev []int
	tw  []complex128
}

// NewFFT prepares a transform of size n, a power of two.
func NewFFT(n int) *FFT {
	f := &FFT{n: n, rev: make([]int, n), tw: make([]complex128, n/2)}
	bits := 0
	for 1<<bits < n {
		bits++
	}
	for i := range f.rev {
		r := 0
		for b := 0; b < bits; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		f.rev[i] = r
	}
	for i := range f.tw {
		f.tw[i] = cmplx.Rect(1, -2*math.Pi*float64(i)/float64(n))
	}
	return f
}

// Transform runs an in-place FFT; the inverse is unscaled.
func (f *FFT) Transform(x []complex128, inverse bool) {
	for i, r := range f.rev {
		if i < r {
			x[i], x[r] = x[r], x[i]
		}
	}
	for size := 2; size <= f.n; size <<= 1 {
		half, step := size/2, f.n/size
		for start := 0; start < f.n; start += size {
			for k := 0; k < half; k++ {
				w := f.tw[k*step]
				if inverse {
					w = cmplx.Conj(w)
				}
				a, b := x[start+k], x[start+k+half]*w
				x[start+k], x[start+k+half] = a+b, a-b
			}
		}
	}
}
