// Package denoise removes steady tape noise (hiss and hum) by spectral
// subtraction. The noise spectrum is measured in the silent stretches of the
// recording itself.
package denoise

import (
	"bufio"
	"context"
	"io"
	"math"
	"math/cmplx"
	"os"

	"github.com/afrunt/tape2digi/internal/dsp"
	"github.com/afrunt/tape2digi/internal/split"
	"github.com/afrunt/tape2digi/internal/wav"
)

const (
	fftSize = 2048
	hop     = fftSize / 4

	minSilenceMs  = 1000  // a silent run this long can serve as a noise sample
	minTotalMs    = 2000  // total noise sample needed
	maxSpreadDB   = 10.0  // allowed level difference between silent runs
	minNoiseDB    = -80.0 // quieter noise is not worth fixing
	learnRunMs    = 20000 // at most this much of one run is measured
	learnTotalMs  = 60000
	overSubtract  = 2.0
	spectralFloor = 0.01 // power gain floor, -20 dB
)

// Plan says where the noise can be measured.
type Plan struct {
	Runs    []split.Span // envelope windows (split.WindowMs each)
	NoiseDB float64      // mean level of the silent stretches, dBFS
}

// Analyze looks for a steady noise pattern in the level envelope (split.WindowMs
// windows, dBFS). It reports false when there is none or it is already low.
func Analyze(env []float32) (Plan, bool) {
	runs, ok := split.Silences(env, minSilenceMs)
	if !ok || len(runs) == 0 {
		return Plan{}, false
	}
	var sum float64
	var count int
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range runs {
		var rs float64
		for _, v := range env[r.From:r.To] {
			rs += math.Pow(10, float64(v)/10)
		}
		sum += rs
		count += r.To - r.From
		db := 10 * math.Log10(rs/float64(r.To-r.From)+1e-12)
		lo, hi = math.Min(lo, db), math.Max(hi, db)
	}
	if count*split.WindowMs < minTotalMs || hi-lo > maxSpreadDB {
		return Plan{}, false
	}
	noise := 10 * math.Log10(sum/float64(count)+1e-12)
	if noise < minNoiseDB {
		return Plan{}, false
	}
	return Plan{Runs: runs, NoiseDB: noise}, true
}

// sqrtHann is the analysis and synthesis window; at 75% overlap its square sums to 2.
func sqrtHann() []float64 {
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = math.Sqrt(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/fftSize))
	}
	return w
}

func sample(b []byte, bps int) float64 {
	switch bps {
	case 2:
		return float64(int16(uint16(b[0]) | uint16(b[1])<<8))
	case 3:
		return float64(int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8)
	default:
		return float64(int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
	}
}

func putSample(b []byte, bps int, v int64) {
	for i := 0; i < bps; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

// profile measures the mean noise power per bin (0..fftSize/2) of every channel.
func profile(src *os.File, format wav.Format, off, frames int64, runs []split.Span) ([][]float64, error) {
	ch, bps := format.Channels, format.BitDepth/8
	frame := int64(format.BlockAlign())
	scale := float64(int64(1) << (format.BitDepth - 1))
	win, tr := sqrtHann(), dsp.NewFFT(fftSize)
	sum := make([][]float64, ch)
	for c := range sum {
		sum[c] = make([]float64, fftSize/2+1)
	}
	msFrames := func(ms int) int64 { return int64(format.SampleRate) * int64(ms) / 1000 }
	count, budget := 0, msFrames(learnTotalMs)
	buf := make([]complex128, fftSize)
	for _, r := range runs {
		from := msFrames(r.From * split.WindowMs)
		n := min(msFrames((r.To-r.From)*split.WindowMs), msFrames(learnRunMs), budget)
		if n < fftSize || from+n > frames {
			continue
		}
		budget -= n
		raw := make([]byte, n*frame)
		if _, err := src.ReadAt(raw, off+from*frame); err != nil {
			return nil, err
		}
		for pos := int64(0); pos+fftSize <= n; pos += fftSize / 2 {
			for c := 0; c < ch; c++ {
				for i := 0; i < fftSize; i++ {
					buf[i] = complex(sample(raw[(pos+int64(i))*frame+int64(c*bps):], bps)/scale*win[i], 0)
				}
				tr.Transform(buf, false)
				for k := range sum[c] {
					a := cmplx.Abs(buf[k])
					sum[c][k] += a * a
				}
			}
			count++
		}
	}
	if count == 0 {
		return nil, wav.ErrNotEditable
	}
	for c := range sum {
		sm := make([]float64, len(sum[c]))
		for k := range sum[c] {
			var s float64
			var m int
			for j := k - 2; j <= k+2; j++ {
				if j >= 0 && j < len(sum[c]) {
					s += sum[c][j]
					m++
				}
			}
			sm[k] = s / float64(m) / float64(count)
		}
		sum[c] = sm
	}
	return sum, nil
}

// channel holds the overlap-add state of one channel.
type channel struct {
	in    []float64
	acc   []float64
	prev  []float64 // bin gains of the previous frame, for smoothing over time
	noise []float64
}

// push adds hop new samples and returns the hop samples that are complete.
func (c *channel) push(block []float64, tr *dsp.FFT, win []float64, buf []complex128, gain []float64) []float64 {
	copy(c.in, c.in[hop:])
	copy(c.in[fftSize-hop:], block)
	for i := range buf {
		buf[i] = complex(c.in[i]*win[i], 0)
	}
	tr.Transform(buf, false)
	half := fftSize / 2
	for k := 0; k <= half; k++ {
		p := real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
		g := spectralFloor
		if p > 0 {
			g = math.Max(spectralFloor, 1-overSubtract*c.noise[k]/p)
		}
		gain[k] = g
	}
	for k := 0; k <= half; k++ {
		var s float64
		var m int
		for j := k - 1; j <= k+1; j++ {
			if j >= 0 && j <= half {
				s += gain[j]
				m++
			}
		}
		g := 0.5*c.prev[k] + 0.5*s/float64(m)
		c.prev[k] = g
		a := complex(math.Sqrt(g), 0)
		buf[k] *= a
		if k > 0 && k < half {
			buf[fftSize-k] *= a
		}
	}
	tr.Transform(buf, true)
	for i := range c.acc {
		c.acc[i] += real(buf[i]) / fftSize * win[i] / 2
	}
	out := make([]float64, hop)
	copy(out, c.acc[:hop])
	copy(c.acc, c.acc[hop:])
	clear(c.acc[fftSize-hop:])
	return out
}

// Process writes a denoised copy of src to dst. The caller owns dst; on error
// or cancellation it is removed.
func Process(ctx context.Context, src, dst string, plan Plan, progress func(done, total int64)) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	format, off, length, err := wav.ReadHeader(in, st.Size())
	if err != nil {
		return err
	}
	frame := int64(format.BlockAlign())
	frames := length / frame
	noise, err := profile(in, format, off, frames, plan.Runs)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	w, err := wav.NewWriter(out, format)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 1<<20)

	ch, bps := format.Channels, format.BitDepth/8
	scale := float64(int64(1) << (format.BitDepth - 1))
	limit := int64(scale) - 1
	win, tr := sqrtHann(), dsp.NewFFT(fftSize)
	buf := make([]complex128, fftSize)
	gain := make([]float64, fftSize/2+1)
	chans := make([]*channel, ch)
	blocks := make([][]float64, ch)
	outs := make([][]float64, ch)
	for c := range chans {
		chans[c] = &channel{in: make([]float64, fftSize), acc: make([]float64, fftSize), prev: make([]float64, fftSize/2+1), noise: noise[c]}
		for k := range chans[c].prev {
			chans[c].prev[k] = 1
		}
		blocks[c] = make([]float64, hop)
	}
	rd := bufio.NewReaderSize(io.NewSectionReader(in, off, length), 1<<20)
	raw := make([]byte, hop*frame)
	line := make([]byte, hop*frame)
	const delay = fftSize/hop - 1 // pushes before the first complete output block
	steps := (frames+hop-1)/hop + delay
	var read, written int64
	for i := int64(0); i < steps; i++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		var n int64
		if read < frames {
			n = min(int64(hop), frames-read)
			if _, err = io.ReadFull(rd, raw[:n*frame]); err != nil {
				return err
			}
			read += n
		}
		for c := 0; c < ch; c++ {
			for j := int64(0); j < hop; j++ {
				blocks[c][j] = 0
				if j < n {
					blocks[c][j] = sample(raw[j*frame+int64(c*bps):], bps) / scale
				}
			}
			outs[c] = chans[c].push(blocks[c], tr, win, buf, gain)
		}
		if i < delay {
			continue
		}
		m := min(int64(hop), frames-written)
		for j := int64(0); j < m; j++ {
			for c := 0; c < ch; c++ {
				v := int64(math.Round(outs[c][j] * scale))
				v = max(-limit-1, min(limit, v))
				putSample(line[j*frame+int64(c*bps):], bps, v)
			}
		}
		if _, err = bw.Write(line[:m*frame]); err != nil {
			return err
		}
		written += m
		if progress != nil {
			progress(written, frames)
		}
	}
	if err = bw.Flush(); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return out.Close()
}
