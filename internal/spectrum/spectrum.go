// Package spectrum computes spectrogram pictures of WAV files: one FFT per
// column of the mono mix, folded into log-spaced frequency rows.
package spectrum

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/afrunt/tape2digi/internal/dsp"
	"github.com/afrunt/tape2digi/internal/wav"
)

const (
	FFTSize  = 2048
	minFFT   = 64
	MinHz    = 20.0
	FloorDB  = -120.0
	MaxCols  = 4000
	MaxRows  = 512
	DefRows  = 128
	levelMax = 255
)

// Picture holds Columns x Rows levels, column by column, row 0 at MinHz. Each
// byte maps FloorDB..0 dBFS linearly onto 0..255.
type Picture struct {
	FromMs  int64   `json:"fromMs"`
	ToMs    int64   `json:"toMs"`
	Columns int     `json:"columns"`
	Rows    int     `json:"rows"`
	MinHz   float64 `json:"minHz"`
	MaxHz   float64 `json:"maxHz"`
	FloorDB float64 `json:"floorDb"`
	Levels  []byte  `json:"levels"`
}

// RowHz is the centre frequency of row r.
func (p Picture) RowHz(r int) float64 { return rowHz(p.MinHz, p.MaxHz, p.Rows, float64(r)+0.5) }

func rowHz(lo, hi float64, rows int, r float64) float64 {
	return lo * math.Pow(hi/lo, r/float64(rows))
}

// Compute analyses [fromMs, toMs) of the file. Each column is one FFT centred
// on its time, read straight from the file, so a whole side costs only
// Columns small reads.
func Compute(ctx context.Context, path string, fromMs, toMs int64, columns, rows int) (Picture, error) {
	f, err := os.Open(path)
	if err != nil {
		return Picture{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Picture{}, err
	}
	format, off, length, err := wav.ReadHeader(f, st.Size())
	if err != nil {
		return Picture{}, err
	}
	if format.BitDepth != 16 && format.BitDepth != 24 && format.BitDepth != 32 {
		return Picture{}, fmt.Errorf("unsupported bit depth %d", format.BitDepth)
	}
	block := int64(format.BlockAlign())
	frames := length / block
	rate := int64(format.SampleRate)
	durMs := frames * 1000 / rate
	fromMs = max(0, min(fromMs, durMs))
	if toMs <= 0 || toMs > durMs {
		toMs = durMs
	}
	columns = max(1, min(columns, MaxCols))
	if rows <= 0 {
		rows = DefRows
	}
	rows = min(rows, MaxRows)
	nyq := float64(format.SampleRate) / 2
	p := Picture{FromMs: fromMs, ToMs: toMs, Columns: columns, Rows: rows, MinHz: MinHz, MaxHz: nyq, FloorDB: FloorDB, Levels: make([]byte, columns*rows)}
	if toMs <= fromMs || frames < minFFT {
		return p, nil
	}
	n := FFTSize
	for n > minFFT && int64(n) > frames {
		n /= 2
	}
	tr := dsp.NewFFT(n)
	win := make([]float64, n)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	// Row r covers bins [lo[r], hi[r]]; narrow low rows take their nearest bin.
	binHz := float64(format.SampleRate) / float64(n)
	lo, hi := make([]int, rows), make([]int, rows)
	for r := 0; r < rows; r++ {
		a := rowHz(MinHz, nyq, rows, float64(r)) / binHz
		b := rowHz(MinHz, nyq, rows, float64(r+1)) / binHz
		lo[r] = min(n/2, int(math.Ceil(a)))
		hi[r] = min(n/2, int(math.Floor(b)))
		if hi[r] < lo[r] {
			c := min(n/2, int(math.Round((a+b)/2)))
			lo[r], hi[r] = c, c
		}
	}
	bps := format.BitDepth / 8
	scale := float64(int64(1) << (format.BitDepth - 1))
	raw := make([]byte, int64(n)*block)
	buf := make([]complex128, n)
	power := make([]float64, n/2+1)
	// A full-scale sine has |X| = n/4 under the Hann window.
	ref := float64(n) / 4
	ref *= ref
	span := toMs - fromMs
	for col := 0; col < columns; col++ {
		if col%64 == 0 {
			if err := ctx.Err(); err != nil {
				return Picture{}, err
			}
		}
		centreMs := float64(fromMs) + (float64(col)+0.5)*float64(span)/float64(columns)
		start := int64(centreMs*float64(rate)/1000) - int64(n/2)
		start = max(0, min(start, frames-int64(n)))
		if _, err := f.ReadAt(raw, off+start*block); err != nil {
			return Picture{}, err
		}
		for i := 0; i < n; i++ {
			var sum float64
			for c := 0; c < format.Channels; c++ {
				sum += float64(wav.SampleAt(raw[int64(i)*block+int64(c*bps):], bps))
			}
			buf[i] = complex(sum/float64(format.Channels)/scale*win[i], 0)
		}
		tr.Transform(buf, false)
		for k := range power {
			re, im := real(buf[k]), imag(buf[k])
			power[k] = re*re + im*im
		}
		row := p.Levels[col*rows : (col+1)*rows]
		for r := 0; r < rows; r++ {
			m := 0.0
			for k := lo[r]; k <= hi[r]; k++ {
				m = max(m, power[k])
			}
			db := 10 * math.Log10(m/ref+1e-30)
			v := (db - FloorDB) / -FloorDB * levelMax
			row[r] = byte(max(0, min(levelMax, math.Round(v))))
		}
	}
	return p, nil
}
