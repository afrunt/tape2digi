package wav

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeSamples(t *testing.T, format Format, samples []int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f, format)
	if err != nil {
		t.Fatal(err)
	}
	bps := format.BitDepth / 8
	b := make([]byte, len(samples)*bps)
	for i, v := range samples {
		putSample(b[i*bps:], bps, v)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSamples(t *testing.T, path string) []int64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	format, off, n, err := ReadHeader(readerAt(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	bps := format.BitDepth / 8
	var out []int64
	for i := int64(0); i+int64(bps) <= n; i += int64(bps) {
		out = append(out, sampleAt(b[off+i:], bps))
	}
	return out
}

type readerAt []byte

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r)) {
		return 0, os.ErrClosed
	}
	return copy(p, r[off:]), nil
}

func TestAnalyzePeaksStereo(t *testing.T) {
	path := writeSamples(t, Format{44100, 2, 16}, []int64{100, -200, 300, -400, 50, 60, -70, 80})
	p, err := Analyze(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frames != 4 || p.Format.Channels != 2 || p.MaxPos != 300 || p.MaxNeg != 400 || len(p.Min) != 2 {
		t.Fatalf("unexpected peaks %+v", p)
	}
	if p.Max[0][0] != 100.0/32768 || p.Min[1][1] != -400.0/32768 {
		t.Fatalf("bucket values %v %v", p.Max[0], p.Min[1])
	}
}

func TestMaxGain(t *testing.T) {
	cases := []struct {
		name         string
		pos, neg     int64
		bits         int
		ok           bool
		wantNum, den int64
	}{
		{"quiet 16", 16383, 8000, 16, true, 32767, 16383},
		{"negative bound", 1000, 16384, 16, true, 32768, 16384},
		{"already max", 32767, 100, 16, false, 0, 0},
		{"negative full scale", 100, 32768, 16, false, 0, 0},
		{"silence", 0, 0, 24, false, 0, 0},
		{"quiet 24", 1 << 20, 1 << 19, 24, true, 1<<23 - 1, 1 << 20},
	}
	for _, c := range cases {
		g, ok := MaxGain(c.pos, c.neg, c.bits)
		if ok != c.ok || (ok && (g.Num != c.wantNum || g.Den != c.den)) {
			t.Errorf("%s: got %+v %v", c.name, g, ok)
		}
	}
}

func TestAmplifyReachesLimitWithoutClipping(t *testing.T) {
	for _, bits := range []int{16, 24} {
		limit := int64(1)<<(bits-1) - 1
		in := []int64{1000, -2000, 3000, -500, 12, 0}
		path := writeSamples(t, Format{44100, 2, bits}, in)
		p, err := Analyze(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		g, ok := MaxGain(p.MaxPos, p.MaxNeg, bits)
		if !ok {
			t.Fatal("gain unavailable")
		}
		dst := filepath.Join(t.TempDir(), "y.wav")
		if err := Amplify(context.Background(), path, dst, g, nil); err != nil {
			t.Fatal(err)
		}
		out := readSamples(t, dst)
		if len(out) != len(in) {
			t.Fatalf("length %d", len(out))
		}
		var max int64
		for _, v := range out {
			if v > limit || v < -limit-1 {
				t.Fatalf("clipped %d", v)
			}
			if v > max {
				max = v
			}
		}
		if max != limit {
			t.Fatalf("%d-bit peak %d, want %d", bits, max, limit)
		}
		p2, _ := Analyze(context.Background(), dst)
		if _, ok := MaxGain(p2.MaxPos, p2.MaxNeg, bits); ok {
			t.Fatal("second amplify should be unavailable")
		}
	}
}

func TestAmplifyMonoAndCancel(t *testing.T) {
	path := writeSamples(t, Format{22050, 1, 16}, []int64{100, -100, 50})
	dst := filepath.Join(t.TempDir(), "m.wav")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Amplify(ctx, path, dst, Gain{2, 1}, nil); err == nil {
		t.Fatal("cancelled amplify must fail")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("partial output must be removed")
	}
	if err := Amplify(context.Background(), path, dst, Gain{2, 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readSamples(t, dst); got[0] != 200 || got[1] != -200 || got[2] != 100 {
		t.Fatalf("got %v", got)
	}
}

func TestReadHeaderSkipsExtraChunk(t *testing.T) {
	path := writeSamples(t, Format{44100, 2, 16}, []int64{1, 2, 3, 4})
	b, _ := os.ReadFile(path)
	extra := append([]byte("LIST"), 4, 0, 0, 0, 'a', 'b', 'c', 'd')
	b = append(append(append([]byte{}, b[:36]...), extra...), b[36:]...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	f, off, n, err := ReadHeader(readerAt(b), int64(len(b)))
	if err != nil || f.Channels != 2 || n != 8 || off != 56 {
		t.Fatalf("%+v %d %d %v", f, off, n, err)
	}
}

func TestRangePeaksMatchesDirectRangeAndClamps(t *testing.T) {
	path := writeSamples(t, Format{44100, 2, 16}, []int64{1, -1, 2, -2, 30, -40, 5, -5, 6, -6, 7, -7})
	mn, mx, err := RangePeaks(context.Background(), path, 1, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if mx[0][1] != 30.0/32768 || mn[1][1] != -40.0/32768 || mx[0][0] != 2.0/32768 || mx[0][2] != 5.0/32768 {
		t.Fatalf("range peaks %v %v", mx, mn)
	}
	mn, mx, err = RangePeaks(context.Background(), path, 4, 100, 50)
	if err != nil || len(mx[0]) != 2 || mx[0][1] != 7.0/32768 {
		t.Fatalf("clamped %v %v", mx, err)
	}
	if _, _, err := RangePeaks(context.Background(), path, 6, 9, 4); err == nil {
		t.Fatal("window past the end must fail")
	}
}

func TestEnvelopeLevelsPerWindow(t *testing.T) {
	// 1000 Hz stereo 24-bit: 50 ms windows are 50 frames; two at half scale, then silence.
	samples := make([]int64, 175*2)
	for i := 0; i < 100*2; i++ {
		samples[i] = 1 << 22
	}
	path := writeSamples(t, Format{1000, 2, 24}, samples)
	env, err := Envelope(context.Background(), path, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 4 {
		t.Fatalf("got %d windows", len(env))
	}
	for i := 0; i < 2; i++ {
		if env[i] < -6.1 || env[i] > -5.9 {
			t.Fatalf("window %d = %v dB, want about -6.02", i, env[i])
		}
	}
	if env[2] != -120 || env[3] != -120 {
		t.Fatalf("silent windows %v", env[2:])
	}
}
