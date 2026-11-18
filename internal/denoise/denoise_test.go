package denoise

import (
	"context"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/afrunt/tape2digi/internal/split"
	"github.com/afrunt/tape2digi/internal/wav"
)

const rate = 44100

// writeTape writes 16-bit mono: noise-only gaps around tone bursts (tone absent when tone is 0).
func writeTape(t *testing.T, path string, noiseAmp, tone float64, pattern []int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(f, wav.Format{SampleRate: rate, Channels: 1, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	n := 0
	for i, secs := range pattern {
		music := i%2 == 1
		buf := make([]byte, 0, secs*rate*2)
		for j := 0; j < secs*rate; j++ {
			v := noiseAmp * rng.NormFloat64()
			if music {
				v += tone * math.Sin(2*math.Pi*440*float64(n)/rate)
			}
			n++
			s := int16(math.Max(-32768, math.Min(32767, v*32768)))
			buf = append(buf, byte(s), byte(uint16(s)>>8))
		}
		if _, err := w.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func rmsDB(t *testing.T, path string, fromS, toS int) float64 {
	t.Helper()
	env, err := wav.Envelope(context.Background(), path, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	from, to := fromS*1000/split.WindowMs, toS*1000/split.WindowMs
	for _, v := range env[from:to] {
		sum += math.Pow(10, float64(v)/10)
	}
	return 10 * math.Log10(sum/float64(to-from))
}

func TestProcessReducesNoiseAndKeepsMusic(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "in.wav"), filepath.Join(dir, "out.wav")
	writeTape(t, src, 0.004, 0.3, []int{4, 12, 4, 12, 4})
	env, err := wav.Envelope(context.Background(), src, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := Analyze(env)
	if !ok {
		t.Fatal("noise pattern not found")
	}
	if err := Process(context.Background(), src, dst, plan, nil); err != nil {
		t.Fatal(err)
	}
	if before, after := rmsDB(t, src, 1, 3), rmsDB(t, dst, 1, 3); before-after < 10 {
		t.Errorf("noise only lowered from %.1f to %.1f dB", before, after)
	}
	if before, after := rmsDB(t, src, 6, 14), rmsDB(t, dst, 6, 14); math.Abs(before-after) > 1 {
		t.Errorf("music changed from %.1f to %.1f dB", before, after)
	}
}

func TestAnalyzeNeedsSilentStretches(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	writeTape(t, src, 0.004, 0.3, []int{0, 30})
	env, err := wav.Envelope(context.Background(), src, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Analyze(env); ok {
		t.Error("continuous music reported as a noise pattern")
	}
}

// TestRealRecording runs against the file named by T2D_SPLIT_SAMPLE and writes
// the result next to it when T2D_DENOISE_OUT is set.
func TestRealRecording(t *testing.T) {
	src := os.Getenv("T2D_SPLIT_SAMPLE")
	if src == "" {
		t.Skip("T2D_SPLIT_SAMPLE not set")
	}
	env, err := wav.Envelope(context.Background(), src, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := Analyze(env)
	if !ok {
		t.Fatal("no noise pattern in the sample")
	}
	t.Logf("noise %.1f dBFS from %d runs", plan.NoiseDB, len(plan.Runs))
	dst := os.Getenv("T2D_DENOISE_OUT")
	if dst == "" {
		return
	}
	if err := Process(context.Background(), src, dst, plan, nil); err != nil {
		t.Fatal(err)
	}
	env, err = wav.Envelope(context.Background(), dst, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	after, again := Analyze(env)
	t.Logf("after: available=%v noise %.1f dBFS", again, after.NoiseDB)
	if again {
		t.Error("processed sample still offers Fix Noise")
	}
}
