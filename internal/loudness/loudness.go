// Package loudness measures integrated loudness after ITU-R BS.1770-4 (EBU
// R 128 gating) and the sample peak of PCM audio. True (oversampled) peak is
// not measured.
package loudness

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"

	"github.com/afrunt/tape2digi/internal/wav"
)

const (
	absoluteGate = -70.0
	relativeGate = -10.0
	// subBlocks of 100 ms make one 400 ms gating block (75% overlap).
	subBlocks = 4
)

// Result holds what is needed to combine measurements: the energies of the
// gating blocks above the absolute gate and the sample peak (0..1).
type Result struct {
	Blocks []float64
	Peak   float64
}

// LUFS returns the integrated loudness; ok is false when nothing is above the
// absolute gate (silence).
func (r Result) LUFS() (float64, bool) {
	if len(r.Blocks) == 0 {
		return 0, false
	}
	var sum float64
	for _, z := range r.Blocks {
		sum += z
	}
	gate := energyLUFS(sum/float64(len(r.Blocks))) + relativeGate
	sum, n := 0, 0
	for _, z := range r.Blocks {
		if energyLUFS(z) > gate {
			sum += z
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return energyLUFS(sum / float64(n)), true
}

// Combine joins measurements of several ranges into one, as if they were one
// programme (album loudness).
func Combine(rs ...Result) Result {
	var out Result
	for _, r := range rs {
		out.Blocks = append(out.Blocks, r.Blocks...)
		out.Peak = math.Max(out.Peak, r.Peak)
	}
	return out
}

func energyLUFS(z float64) float64 { return -0.691 + 10*math.Log10(z) }

// biquad is a direct form I second-order filter.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func (f *biquad) step(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1, f.y2, f.y1 = f.x1, x, f.y1, y
	return y
}

// kWeighting returns the BS.1770 pre-filter (high shelf) and RLB high-pass
// for the sample rate, derived from the analogue prototypes.
func kWeighting(rate int) (shelf, hp biquad) {
	fs := float64(rate)
	{
		const f0, g, q = 1681.974450955533, 3.999843853973347, 0.7071752369554196
		k := math.Tan(math.Pi * f0 / fs)
		vh := math.Pow(10, g/20)
		vb := math.Pow(vh, 0.4996667741545416)
		a0 := 1 + k/q + k*k
		shelf = biquad{
			b0: (vh + vb*k/q + k*k) / a0, b1: 2 * (k*k - vh) / a0, b2: (vh - vb*k/q + k*k) / a0,
			a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0,
		}
	}
	{
		const f0, q = 38.13547087602444, 0.5003270373238773
		k := math.Tan(math.Pi * f0 / fs)
		a0 := 1 + k/q + k*k
		hp = biquad{b0: 1, b1: -2, b2: 1, a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0}
	}
	return shelf, hp
}

// Meter accumulates interleaved PCM samples.
type Meter struct {
	channels int
	filters  [][2]biquad
	sub      int // samples per channel in a 100 ms sub-block
	n        int // samples per channel in the current sub-block
	acc      float64
	ring     [subBlocks]float64
	filled   int
	res      Result
}

// NewMeter returns a meter for the sample rate and channel count.
func NewMeter(rate, channels int) *Meter {
	m := &Meter{channels: channels, sub: rate / 10, filters: make([][2]biquad, channels)}
	for c := range m.filters {
		s, h := kWeighting(rate)
		m.filters[c] = [2]biquad{s, h}
	}
	return m
}

// Frame adds one frame of samples in -1..1, one per channel.
func (m *Meter) Frame(samples []float64) {
	for c, x := range samples {
		if a := math.Abs(x); a > m.res.Peak {
			m.res.Peak = a
		}
		f := &m.filters[c]
		y := f[1].step(f[0].step(x))
		m.acc += y * y
	}
	m.n++
	if m.n < m.sub {
		return
	}
	copy(m.ring[:], m.ring[1:])
	m.ring[subBlocks-1] = m.acc / float64(m.sub)
	m.acc, m.n = 0, 0
	if m.filled++; m.filled >= subBlocks {
		var z float64
		for _, v := range m.ring {
			z += v
		}
		z /= subBlocks
		if z > 0 && energyLUFS(z) > absoluteGate {
			m.res.Blocks = append(m.res.Blocks, z)
		}
	}
}

// Result returns the measurement so far.
func (m *Meter) Result() Result { return m.res }

// MeasurePCM reads interleaved little-endian PCM in format f from r.
// progress, when set, receives the bytes read so far.
func MeasurePCM(ctx context.Context, r io.Reader, f wav.Format, progress func(n int64)) (Result, error) {
	m := NewMeter(f.SampleRate, f.Channels)
	bps := f.BitDepth / 8
	ba := f.BlockAlign()
	scale := math.Ldexp(1, f.BitDepth-1)
	buf := make([]byte, 64*1024-64*1024%ba)
	frame := make([]float64, f.Channels)
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		n, err := io.ReadFull(r, buf)
		n -= n % ba
		for o := 0; o < n; o += ba {
			for c := range frame {
				frame[c] = float64(sample(buf[o+c*bps:], bps)) / scale
			}
			m.Frame(frame)
		}
		done += int64(n)
		if progress != nil && n > 0 {
			progress(done)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return m.Result(), nil
		}
		if err != nil {
			return Result{}, err
		}
	}
}

func sample(b []byte, bps int) int64 {
	switch bps {
	case 1:
		return int64(b[0]) - 128
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(b)))
	case 3:
		return int64(int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8)
	default:
		return int64(int32(binary.LittleEndian.Uint32(b)))
	}
}

// MeasureFile measures frames [from, from+frames) of a WAV file; frames < 0
// means to the end.
func MeasureFile(ctx context.Context, path string, from, frames int64, progress func(n int64)) (Result, wav.Format, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Result{}, wav.Format{}, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return Result{}, wav.Format{}, err
	}
	f, off, length, err := wav.ReadHeader(fh, st.Size())
	if err != nil {
		return Result{}, wav.Format{}, err
	}
	ba := int64(f.BlockAlign())
	start := min(max(from, 0)*ba, length)
	n := length - start
	if frames >= 0 {
		n = min(n, frames*ba)
	}
	res, err := MeasurePCM(ctx, io.NewSectionReader(fh, off+start, n), f, progress)
	return res, f, err
}

// Normalize limits.
const (
	MinTarget     = -23.0
	MaxTarget     = -9.0
	DefaultTarget = -18.0
	// PeakCeiling is the highest sample peak normalising may produce, in dBFS.
	PeakCeiling = -1.0
	// MinGain is the smallest change worth applying, in dB.
	MinGain = 0.1
)

// NormalizeGain returns the gain in dB that brings measured loudness to the
// target, lowered so the sample peak (0..1) stays at or below PeakCeiling;
// capped tells whether the ceiling limited it.
func NormalizeGain(measured, peak, target float64) (gain float64, capped bool) {
	gain = target - measured
	if peak > 0 {
		if room := PeakCeiling - 20*math.Log10(peak); gain > room {
			return room, true
		}
	}
	return gain, false
}
