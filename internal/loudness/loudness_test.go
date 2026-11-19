package loudness

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/afrunt/tape2digi/internal/wav"
)

// sine returns stereo 16-bit PCM of a sine at dbfs peak level.
func sine(rate int, hz, dbfs, seconds float64) []byte {
	amp := math.Pow(10, dbfs/20) * 32767
	n := int(float64(rate) * seconds)
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		v := int16(math.Round(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))))
		_ = binary.Write(&b, binary.LittleEndian, [2]int16{v, v})
	}
	return b.Bytes()
}

func measure(t *testing.T, pcm []byte, rate int) Result {
	t.Helper()
	r, err := MeasurePCM(context.Background(), bytes.NewReader(pcm), wav.Format{SampleRate: rate, Channels: 2, BitDepth: 16}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSineMatchesReference(t *testing.T) {
	// EBU Tech 3341 case 1: stereo 1 kHz at -23 dBFS reads -23.0 LUFS.
	for _, rate := range []int{44100, 48000} {
		l, ok := measure(t, sine(rate, 1000, -23, 20), rate).LUFS()
		if !ok || math.Abs(l+23) > 0.1 {
			t.Fatalf("%d Hz: %.2f LUFS", rate, l)
		}
	}
	r := measure(t, sine(48000, 1000, -20, 10), 48000)
	if l, _ := r.LUFS(); math.Abs(l+20) > 0.1 {
		t.Fatalf("-20 dBFS: %.2f LUFS", l)
	}
	if math.Abs(r.Peak-0.1) > 0.001 {
		t.Fatalf("peak %f", r.Peak)
	}
}

func TestGatingAndSilence(t *testing.T) {
	if _, ok := measure(t, make([]byte, 48000*4*5), 48000).LUFS(); ok {
		t.Fatal("silence has loudness")
	}
	// EBU Tech 3341 case 3 style: quiet parts below the relative gate do not count.
	pcm := append(append(sine(48000, 1000, -36, 10), sine(48000, 1000, -23, 60)...), sine(48000, 1000, -36, 10)...)
	if l, _ := measure(t, pcm, 48000).LUFS(); math.Abs(l+23) > 0.1 {
		t.Fatalf("gated %.2f", l)
	}
	a, b := measure(t, sine(48000, 1000, -23, 10), 48000), measure(t, sine(48000, 1000, -33, 10), 48000)
	if l, _ := Combine(a, b).LUFS(); l > -23 || l < -27 {
		t.Fatalf("album %.2f", l)
	}
}

func TestNormalizeGainCapsAtPeakCeiling(t *testing.T) {
	if g, c := NormalizeGain(-24, 0.25, -18); c || math.Abs(g-6) > 1e-9 {
		t.Fatalf("free: %.2f %v", g, c)
	}
	// Peak at -6 dBFS leaves 5 dB before -1 dBFS.
	if g, c := NormalizeGain(-30, 0.5, -18); !c || math.Abs(g-(-1-20*math.Log10(0.5))) > 1e-9 {
		t.Fatalf("capped: %.2f %v", g, c)
	}
	if g, c := NormalizeGain(-12, 0.9, -18); c || math.Abs(g+6) > 1e-9 {
		t.Fatalf("lowering: %.2f %v", g, c)
	}
	// Already over the ceiling: the cap lowers the level.
	if g, c := NormalizeGain(-18, 1, -18); !c || math.Abs(g+1) > 1e-9 {
		t.Fatalf("hot peak: %.2f %v", g, c)
	}
}
