package repair

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/afrunt/tape2digi/internal/split"
	"github.com/afrunt/tape2digi/internal/wav"
)

const rate = 44100

// write stores stereo 16-bit frames given by gen(i) (values -1..1).
func write(t *testing.T, frames int, gen func(i int) (float64, float64)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(f, wav.Format{SampleRate: rate, Channels: 2, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 0, frames*4)
	for i := 0; i < frames; i++ {
		l, r := gen(i)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(math.Round(l*32767))))
		buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(math.Round(r*32767))))
	}
	_, _ = w.Write(buf)
	_ = w.Close()
	_ = f.Close()
	return path
}

func read(t *testing.T, path string) (l, r []float64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 44; i+4 <= len(b); i += 4 {
		l = append(l, float64(int16(binary.LittleEndian.Uint16(b[i:])))/32768)
		r = append(r, float64(int16(binary.LittleEndian.Uint16(b[i+2:])))/32768)
	}
	return l, r
}

func analyze(t *testing.T, path string) Analysis {
	t.Helper()
	env, err := wav.Envelope(context.Background(), path, split.WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(context.Background(), path, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func apply(t *testing.T, src string, op Op) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "out.wav")
	if err := Process(context.Background(), src, dst, op, nil); err != nil {
		t.Fatal(err)
	}
	return dst
}

func sine(hz float64, i int) float64 { return math.Sin(2 * math.Pi * hz * float64(i) / rate) }

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

func level(x []float64, hz float64) float64 {
	return 10 * math.Log10(goertzel(x, rate, hz)+1e-30)
}

// tone is 3 s of quiet, 6 s of music, 3 s of quiet with optional extras.
func tone(extra func(i int) float64) func(i int) (float64, float64) {
	return func(i int) (float64, float64) {
		v := extra(i)
		if i >= 3*rate && i < 9*rate {
			v += 0.4 * sine(1000, i)
		}
		return v, v
	}
}

func TestDCOffsetIsRemoved(t *testing.T) {
	src := write(t, 12*rate, tone(func(int) float64 { return 0.02 }))
	a := analyze(t, src)
	if !a.DCFix || math.Abs(a.DC[0]-0.02) > 0.001 {
		t.Fatalf("analysis %+v", a)
	}
	l, _ := read(t, apply(t, src, a.Op(ToolDC)))
	var mean float64
	for _, v := range l[rate:] {
		mean += v
	}
	if mean /= float64(len(l) - rate); math.Abs(mean) > 0.0002 {
		t.Fatalf("mean after %f", mean)
	}
	if after := analyze(t, src); after.DCFix != a.DCFix {
		t.Fatal("analysis must be repeatable")
	}
}

func TestHumIsNotchedAndMusicKept(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	src := write(t, 12*rate, tone(func(i int) float64 {
		return 0.01*sine(50, i) + 0.004*sine(150, i) + 0.0005*rng.NormFloat64()
	}))
	a := analyze(t, src)
	if a.HumHz != 50 || a.HumDB > -35 || a.HumDB < -45 {
		t.Fatalf("hum %d Hz %.1f dB", a.HumHz, a.HumDB)
	}
	inL, _ := read(t, src)
	outL, _ := read(t, apply(t, src, a.Op(ToolHum)))
	quiet := func(x []float64) []float64 { return x[10*rate : 12*rate] }
	music := func(x []float64) []float64 { return x[5*rate : 7*rate] }
	if d := level(quiet(inL), 50) - level(quiet(outL), 50); d < 20 {
		t.Fatalf("hum reduced by %.1f dB", d)
	}
	if d := math.Abs(level(music(inL), 1000) - level(music(outL), 1000)); d > 0.5 {
		t.Fatalf("1 kHz changed by %.2f dB", d)
	}
	clean := write(t, 12*rate, tone(func(i int) float64 { return 0.0005 * rng.NormFloat64() }))
	if a := analyze(t, clean); a.HumHz != 0 {
		t.Fatalf("hum found in noise: %d", a.HumHz)
	}
}

func TestBalanceIsCorrected(t *testing.T) {
	g := math.Pow(10, -3.0/20)
	src := write(t, 6*rate, func(i int) (float64, float64) {
		v := 0.3 * (sine(440, i) + 0.5*sine(1234, i))
		return v * g, v
	})
	a := analyze(t, src)
	if !a.BalanceFix || math.Abs(a.BalanceDB-3) > 0.05 {
		t.Fatalf("balance %.2f", a.BalanceDB)
	}
	l, r := read(t, apply(t, src, a.Op(ToolBalance)))
	if d := 20 * math.Log10(rms(r)/rms(l)); math.Abs(d) > 0.1 {
		t.Fatalf("after %.2f dB", d)
	}
	if c, db := BalanceGain(5, 0.9, 0.5); c != 0 || math.Abs(db+20*math.Log10(0.9)) > 1e-9 {
		t.Fatalf("cap: %d %.2f", c, db)
	}
}

func TestAzimuthDelayIsFoundAndRemoved(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	noise := make([]float64, 6*rate+5)
	for i := range noise {
		noise[i] = 0.2 * rng.NormFloat64()
	}
	// The right channel lags the left by 5 samples.
	src := write(t, 6*rate, func(i int) (float64, float64) { return noise[i+5], noise[i] })
	a := analyze(t, src)
	if !a.DelayFix || a.Delay != 5 {
		t.Fatalf("delay %d corr %.2f fix %v", a.Delay, a.Corr, a.DelayFix)
	}
	l, r := read(t, apply(t, src, a.Op(ToolAzimuth)))
	if len(l) != 6*rate {
		t.Fatalf("length %d", len(l))
	}
	for i := 0; i < len(l)-5; i++ {
		if l[i] != r[i] {
			t.Fatalf("frame %d differs: %f %f", i, l[i], r[i])
		}
	}
	if b := analyze(t, write(t, 6*rate, func(i int) (float64, float64) { return noise[i], noise[i] })); b.DelayFix {
		t.Fatal("aligned channels must not be shifted")
	}
}

func TestMonoModesMakeIdenticalChannels(t *testing.T) {
	src := write(t, rate, func(i int) (float64, float64) { return 0.3 * sine(440, i), 0.1 * sine(880, i) })
	inL, inR := read(t, src)
	for _, mode := range []string{MonoLeft, MonoRight, MonoMix} {
		l, r := read(t, apply(t, src, Op{Tool: ToolMono, Mono: mode}))
		for i := range l {
			want := map[string]float64{MonoLeft: inL[i], MonoRight: inR[i], MonoMix: (inL[i] + inR[i]) / 2}[mode]
			if l[i] != r[i] || math.Abs(l[i]-want) > 1.0/32768 {
				t.Fatalf("%s frame %d: %f %f want %f", mode, i, l[i], r[i], want)
			}
		}
	}
	if err := Process(context.Background(), src, filepath.Join(t.TempDir(), "x.wav"), Op{Tool: ToolMono, Mono: "up"}, nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
