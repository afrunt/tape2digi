// Package repair analyses and fixes common tape faults: DC offset, mains
// hum, channel imbalance, azimuth (inter-channel delay) and a dead channel.
// Everything streams with constant memory.
package repair

import (
	"bufio"
	"context"
	"io"
	"math"
	"os"

	"github.com/afrunt/tape2digi/internal/split"
	"github.com/afrunt/tape2digi/internal/wav"
)

// Tools.
const (
	ToolDC      = "dc"
	ToolHum     = "hum"
	ToolBalance = "balance"
	ToolAzimuth = "azimuth"
	ToolMono    = "mono"
)

// Mono modes.
const (
	MonoLeft  = "left"
	MonoRight = "right"
	MonoMix   = "mix"
)

const (
	minDC         = 0.001 // 0.1% of full scale
	dcCutHz       = 5.0
	humHarmonics  = 5
	humQ          = 30.0
	humMinRatioDB = 10.0
	humNeighbour  = 8.0 // Hz either side of a harmonic used as reference
	minBalanceDB  = 0.5
	MaxBalanceDB  = 6.0
	azimuthMaxMs  = 1.0
	azimuthHPHz   = 2000.0
	azimuthEvery  = 4 // correlate one block out of this many
	azimuthBlock  = 4096
	minCorr       = 0.5 // normalised correlation needed at the best lag
	minCorrMargin = 0.05
	quietMinMs    = 1000
)

// Analysis is what one pass over a side found.
type Analysis struct {
	Channels int `json:"channels"`
	// DC is the mean of each channel as a share of full scale.
	DC    []float64 `json:"dc"`
	DCFix bool      `json:"dcFix"`
	// HumHz is 50 or 60 when mains hum stands out in the quiet stretches.
	HumHz int     `json:"humHz"`
	HumDB float64 `json:"humDb"` // hum level in the quiet stretches, dBFS
	// BalanceDB is right minus left RMS over the music.
	BalanceDB  float64 `json:"balanceDb"`
	BalanceFix bool    `json:"balanceFix"`
	// Delay is how many samples the right channel lags the left (negative: leads).
	Delay    int     `json:"delay"`
	DelayFix bool    `json:"delayFix"`
	Corr     float64 `json:"corr"` // normalised correlation at Delay
	PeakL    float64 `json:"-"`
	PeakR    float64 `json:"-"`
	frames   int64
}

// Op is one repair to apply.
type Op struct {
	Tool string
	// DC are the per-channel offsets (share of full scale) to prime the filter.
	DC    []float64
	HumHz int
	// BalanceDB is right minus left; the quieter channel is raised by it.
	BalanceDB float64
	Delay     int
	Mono      string
	// Peaks of the left and right channel (0..1) bound the balance gain.
	PeakL, PeakR float64
}

type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func (f *biquad) step(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1, f.y2, f.y1 = f.x1, x, f.y1, y
	return y
}

func highpass(rate int, hz, q float64) biquad {
	w := 2 * math.Pi * hz / float64(rate)
	alpha := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + alpha
	return biquad{b0: (1 + c) / 2 / a0, b1: -(1 + c) / a0, b2: (1 + c) / 2 / a0, a1: -2 * c / a0, a2: (1 - alpha) / a0}
}

func notch(rate int, hz, q float64) biquad {
	w := 2 * math.Pi * hz / float64(rate)
	alpha := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + alpha
	return biquad{b0: 1 / a0, b1: -2 * c / a0, b2: 1 / a0, a1: -2 * c / a0, a2: (1 - alpha) / a0}
}

// goertzel returns the power at hz of a block.
func goertzel(x []float64, rate int, hz float64) float64 {
	w := 2 * math.Pi * hz / float64(rate)
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range x {
		s1, s2 = v+c*s1-s2, s1
	}
	return s1*s1 + s2*s2 - c*s1*s2
}

// reader streams frames of a WAV file as float64 in -1..1.
type reader struct {
	format       wav.Format
	frames       int64
	in           *os.File
	rd           *bufio.Reader
	raw          []byte
	scale        float64
	bps, ba      int
	done         int64
	progress     func(done, total int64)
	sinceReport  int64
	reportFrames int64
}

func open(path string, progress func(done, total int64)) (*reader, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := in.Stat()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	f, off, length, err := wav.ReadHeader(in, st.Size())
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	ba := f.BlockAlign()
	return &reader{
		format: f, frames: length / int64(ba), in: in,
		rd:  bufio.NewReaderSize(io.NewSectionReader(in, off, length), 1<<20),
		raw: make([]byte, ba), scale: math.Ldexp(1, f.BitDepth-1), bps: f.BitDepth / 8, ba: ba,
		progress: progress, reportFrames: int64(f.SampleRate),
	}, nil
}

// next fills x with the next frame; it returns false at the end.
func (r *reader) next(x []float64) (bool, error) {
	if r.done >= r.frames {
		return false, nil
	}
	if _, err := io.ReadFull(r.rd, r.raw); err != nil {
		return false, err
	}
	for c := range x {
		x[c] = float64(wav.SampleAt(r.raw[c*r.bps:], r.bps)) / r.scale
	}
	r.done++
	if r.sinceReport++; r.sinceReport >= r.reportFrames && r.progress != nil {
		r.sinceReport = 0
		r.progress(r.done, r.frames)
	}
	return true, nil
}

// inRuns tracks whether the frame position is inside one of the envelope runs.
type inRuns struct {
	runs          []split.Span
	i             int
	framesPerWind int64
}

func (q *inRuns) at(frame int64) bool {
	w := int(frame / q.framesPerWind)
	for q.i < len(q.runs) && q.runs[q.i].To <= w {
		q.i++
	}
	return q.i < len(q.runs) && q.runs[q.i].From <= w
}

// Analyze measures the side at path; env is its level envelope
// (split.WindowMs windows) used to find the quiet stretches.
func Analyze(ctx context.Context, path string, env []float32, progress func(done, total int64)) (Analysis, error) {
	r, err := open(path, progress)
	if err != nil {
		return Analysis{}, err
	}
	defer r.in.Close()
	f := r.format
	ch := f.Channels
	quiet, _ := split.Silences(env, quietMinMs)
	runs := inRuns{runs: quiet, framesPerWind: max(int64(f.SampleRate*split.WindowMs/1000), 1)}

	sum := make([]float64, ch)
	peak := make([]float64, ch)
	var musicE [2]float64
	var allE [2]float64

	// Hum: one-second Hann-windowed blocks of the mono mix inside quiet runs.
	block := make([]float64, 0, f.SampleRate)
	win := make([]float64, f.SampleRate)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(win)-1))
	}
	freqs := []float64{50, 60}
	var humP, humRef [2]float64
	humBlocks := 0
	flushHum := func() {
		if len(block) == cap(block) {
			for i := range block {
				block[i] *= win[i]
			}
			for fi, base := range freqs {
				for k := 1; k <= humHarmonics; k++ {
					h := base * float64(k)
					if h+humNeighbour >= float64(f.SampleRate)/2 {
						break
					}
					humP[fi] += goertzel(block, f.SampleRate, h)
					humRef[fi] += (goertzel(block, f.SampleRate, h-humNeighbour) + goertzel(block, f.SampleRate, h+humNeighbour)) / 2
				}
			}
			humBlocks++
		}
		block = block[:0]
	}

	// Azimuth: high-passed channels and a sliding correlation over ±maxLag.
	stereo := ch == 2
	maxLag := int(float64(f.SampleRate) * azimuthMaxMs / 1000)
	hpL, hpR := highpass(f.SampleRate, azimuthHPHz, 0.7071), highpass(f.SampleRate, azimuthHPHz, 0.7071)
	ring := 2*maxLag + 1
	bufL, bufR := make([]float64, ring), make([]float64, ring)
	corr := make([]float64, ring)
	var eL, eR float64
	var pos int

	x := make([]float64, ch)
	var frame int64
	for {
		if frame%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return Analysis{}, err
			}
		}
		ok, err := r.next(x)
		if err != nil {
			return Analysis{}, err
		}
		if !ok {
			break
		}
		var mono float64
		for c, v := range x {
			sum[c] += v
			peak[c] = math.Max(peak[c], math.Abs(v))
			mono += v
		}
		mono /= float64(ch)
		isQuiet := runs.at(frame)
		if isQuiet {
			block = append(block, mono)
			if len(block) == cap(block) {
				flushHum()
			}
		} else if len(block) > 0 {
			block = block[:0]
		}
		if stereo {
			for c := 0; c < 2; c++ {
				allE[c] += x[c] * x[c]
				if !isQuiet {
					musicE[c] += x[c] * x[c]
				}
			}
			l, rr := hpL.step(x[0]), hpR.step(x[1])
			bufL[pos], bufR[pos] = l, rr
			if !isQuiet && (frame/azimuthBlock)%azimuthEvery == 0 && frame >= int64(ring) {
				// Centre sample of L (maxLag ago) against R at every lag.
				cl := bufL[(pos-maxLag+ring)%ring]
				eL += cl * cl
				eR += bufR[(pos-maxLag+ring)%ring] * bufR[(pos-maxLag+ring)%ring]
				for k := 0; k < ring; k++ {
					// lag d = k - maxLag: R[n+d] with n the centre.
					corr[k] += cl * bufR[(pos-maxLag+k-maxLag+2*ring)%ring]
				}
			}
			pos = (pos + 1) % ring
		}
		frame++
	}
	a := Analysis{Channels: ch, DC: make([]float64, ch), frames: frame}
	if frame == 0 {
		return a, nil
	}
	for c := range sum {
		a.DC[c] = sum[c] / float64(frame)
		a.DCFix = a.DCFix || math.Abs(a.DC[c]) > minDC
	}
	if humBlocks > 0 {
		best := -1
		bestRatio := humMinRatioDB
		for fi := range freqs {
			if humRef[fi] <= 0 || humP[fi] <= 0 {
				continue
			}
			if ratio := 10 * math.Log10(humP[fi]/humRef[fi]); ratio >= bestRatio {
				best, bestRatio = fi, ratio
			}
		}
		if best >= 0 {
			a.HumHz = int(freqs[best])
			// Hann-windowed Goertzel power of a sine of amplitude A is (A*N/4)^2.
			n := float64(f.SampleRate)
			amp := math.Sqrt(humP[best]/float64(humBlocks)) * 4 / n
			a.HumDB = 20 * math.Log10(amp+1e-12)
		}
	}
	if stereo {
		a.PeakL, a.PeakR = peak[0], peak[1]
		e := musicE
		if e[0] == 0 || e[1] == 0 {
			e = allE
		}
		if e[0] > 0 && e[1] > 0 {
			a.BalanceDB = 10 * math.Log10(e[1]/e[0])
			a.BalanceFix = math.Abs(a.BalanceDB) >= minBalanceDB
		}
		if eL > 0 && eR > 0 {
			norm := math.Sqrt(eL * eR)
			best := maxLag
			for k := range corr {
				if corr[k] > corr[best] {
					best = k
				}
			}
			a.Delay = best - maxLag
			a.Corr = corr[best] / norm
			a.DelayFix = a.Delay != 0 && a.Corr >= minCorr && a.Corr-corr[maxLag]/norm >= minCorrMargin
		}
	}
	return a, nil
}

// Op returns the operation that fixes the analysed fault with tool.
func (a Analysis) Op(tool string) Op {
	return Op{Tool: tool, DC: a.DC, HumHz: a.HumHz, BalanceDB: a.BalanceDB, Delay: a.Delay, PeakL: a.PeakL, PeakR: a.PeakR}
}

// BalanceGain returns the gain in dB for the quieter channel (0 left, 1
// right), capped so that channel cannot clip.
func BalanceGain(balanceDB, peakL, peakR float64) (channel int, gainDB float64) {
	channel, gainDB, peak := 0, balanceDB, peakL
	if balanceDB < 0 {
		channel, gainDB, peak = 1, -balanceDB, peakR
	}
	if peak > 0 {
		gainDB = math.Min(gainDB, -20*math.Log10(peak))
	}
	return channel, math.Max(gainDB, 0)
}

// Process writes a repaired copy of src to dst. The caller owns dst; on error
// or cancellation it is removed.
func Process(ctx context.Context, src, dst string, op Op, progress func(done, total int64)) (err error) {
	r, err := open(src, progress)
	if err != nil {
		return err
	}
	defer r.in.Close()
	f := r.format
	ch := f.Channels
	if ch != 2 && (op.Tool == ToolBalance || op.Tool == ToolAzimuth || op.Tool == ToolMono) {
		return errStereoOnly
	}
	step, err := tool(op, f)
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
	w, err := wav.NewWriter(out, f)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	limit := int64(r.scale) - 1
	line := make([]byte, r.ba)
	emit := func(y []float64) error {
		for c, v := range y {
			s := int64(math.Round(v * r.scale))
			wav.PutSample(line[c*r.bps:], r.bps, max(-limit-1, min(limit, s)))
		}
		_, e := bw.Write(line)
		return e
	}
	x := make([]float64, ch)
	for i := int64(0); ; i++ {
		if i%65536 == 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		var ok bool
		if ok, err = r.next(x); err != nil {
			return err
		}
		if !ok {
			break
		}
		if err = step.frame(x, emit); err != nil {
			return err
		}
	}
	if err = step.end(emit); err != nil {
		return err
	}
	if progress != nil {
		progress(r.frames, r.frames)
	}
	if err = bw.Flush(); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return out.Close()
}
