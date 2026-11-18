package exporter

import (
	"context"
	"math"
)

// WAVRates are the target sample rates the dialog offers for WAV files.
var WAVRates = []int{44100, 48000, 88200, 96000}

const sincLobes = 16

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// resampler converts interleaved little-endian PCM with a windowed-sinc filter
// that low-passes below the lower of the two Nyquist frequencies.
type resampler struct {
	from, to   int64
	channels   int
	bytes      int
	cutoff     float64
	half       int
	phases     int64
	step       int64
	tables     [][]float64
	src        []byte
	srcFrames  int64
	scale      float64
	blockAlign int
}

func newResampler(src []byte, from, to, channels, bitDepth int) *resampler {
	g := gcd(int64(from), int64(to))
	r := &resampler{
		from: int64(from), to: int64(to), channels: channels, bytes: bitDepth / 8,
		cutoff: math.Min(1, float64(to)/float64(from)),
		phases: int64(to) / g, step: int64(from) / g,
		src:   src,
		scale: float64(int64(1)<<(bitDepth-1)) - 1,
	}
	r.half = int(math.Ceil(sincLobes / r.cutoff))
	r.tables = make([][]float64, r.phases)
	r.blockAlign = channels * r.bytes
	r.srcFrames = int64(len(src) / r.blockAlign)
	return r
}

// outFrames is the number of frames after conversion.
func (r *resampler) outFrames() int64 {
	return (r.srcFrames*r.to + r.from/2) / r.from
}

// weights returns the normalised filter taps for a fractional source position
// of phase/phases frames past an integer frame.
func (r *resampler) weights(phase int64) []float64 {
	if w := r.tables[phase]; w != nil {
		return w
	}
	frac := float64(phase) / float64(r.phases)
	n := 2 * r.half
	w := make([]float64, n)
	sum := 0.0
	for k := 0; k < n; k++ {
		x := float64(k-r.half+1) - frac
		v := r.cutoff * sinc(r.cutoff*x) * blackman(x/float64(r.half))
		w[k] = v
		sum += v
	}
	for k := range w {
		w[k] /= sum
	}
	r.tables[phase] = w
	return w
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// blackman is the window over u in [-1, 1].
func blackman(u float64) float64 {
	if u <= -1 || u >= 1 {
		return 0
	}
	p := math.Pi * (u + 1)
	return 0.42 - 0.5*math.Cos(p) + 0.08*math.Cos(2*p)
}

func (r *resampler) sample(frame int64, ch int) float64 {
	if frame < 0 || frame >= r.srcFrames {
		return 0
	}
	o := int(frame)*r.blockAlign + ch*r.bytes
	var v int32
	switch r.bytes {
	case 2:
		v = int32(int16(uint16(r.src[o]) | uint16(r.src[o+1])<<8))
	case 3:
		v = int32(uint32(r.src[o])<<8|uint32(r.src[o+1])<<16|uint32(r.src[o+2])<<24) >> 8
	default:
		v = int32(uint32(r.src[o]) | uint32(r.src[o+1])<<8 | uint32(r.src[o+2])<<16 | uint32(r.src[o+3])<<24)
	}
	return float64(v)
}

// run converts the audio and hands it on in blocks of whole frames.
func (r *resampler) run(ctx context.Context, emit func([]byte) error) error {
	total := r.outFrames()
	const block = 8192
	buf := make([]byte, 0, block*r.blockAlign)
	for i := int64(0); i < total; i++ {
		if i%block == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		num := i * r.step
		pos, phase := num/r.phases, num%r.phases
		w := r.weights(phase)
		for ch := 0; ch < r.channels; ch++ {
			acc := 0.0
			base := pos - int64(r.half) + 1
			for k, wk := range w {
				acc += wk * r.sample(base+int64(k), ch)
			}
			v := int32(math.Round(math.Max(-r.scale-1, math.Min(r.scale, acc))))
			switch r.bytes {
			case 2:
				buf = append(buf, byte(v), byte(v>>8))
			case 3:
				buf = append(buf, byte(v), byte(v>>8), byte(v>>16))
			default:
				buf = append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
			}
		}
		if len(buf) >= block*r.blockAlign {
			if err := emit(buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		return emit(buf)
	}
	return nil
}
