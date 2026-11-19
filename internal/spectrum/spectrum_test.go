package spectrum

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/afrunt/tape2digi/internal/wav"
)

func TestSinePeaksInItsRow(t *testing.T) {
	const rate, hz = 44100, 1000.0
	path := filepath.Join(t.TempDir(), "s.wav")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(fh, wav.Format{SampleRate: rate, Channels: 2, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 0, rate*2*4)
	for i := 0; i < rate*2; i++ {
		v := uint16(int16(math.Round(16000 * math.Sin(2*math.Pi*hz*float64(i)/rate))))
		buf = binary.LittleEndian.AppendUint16(buf, v)
		buf = binary.LittleEndian.AppendUint16(buf, v)
	}
	_, _ = w.Write(buf)
	_ = w.Close()
	_ = fh.Close()
	p, err := Compute(context.Background(), path, 0, 0, 20, 128)
	if err != nil {
		t.Fatal(err)
	}
	if p.Columns != 20 || len(p.Levels) != 20*128 {
		t.Fatalf("shape %d %d", p.Columns, len(p.Levels))
	}
	for c := 0; c < p.Columns; c++ {
		col := p.Levels[c*p.Rows : (c+1)*p.Rows]
		best := 0
		for r := range col {
			if col[r] > col[best] {
				best = r
			}
		}
		lo, hi := rowHz(p.MinHz, p.MaxHz, p.Rows, float64(best)), rowHz(p.MinHz, p.MaxHz, p.Rows, float64(best+1))
		if hz < lo*0.97 || hz > hi*1.03 {
			t.Fatalf("column %d peaks at row %d (%.0f-%.0f Hz)", c, best, lo, hi)
		}
		// About -6 dBFS (16000 of 32768).
		db := float64(col[best])/255*-FloorDB + FloorDB
		if db < -9 || db > -4 {
			t.Fatalf("peak level %.1f dB", db)
		}
	}
}
