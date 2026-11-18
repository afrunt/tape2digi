package importer

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/afrunt/tape2digi/internal/i18n"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/afrunt/tape2digi/internal/wav"
)

// readPCM returns the format and de-interleaved samples (scaled to [-1,1]) of a WAV written by Convert.
func readPCM(t *testing.T, path string) (wav.Format, [][]float64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, data, err := wav.Inspect(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	bps := f.BitDepth / 8
	out := make([][]float64, f.Channels)
	for i := 0; i < int(data)/f.BlockAlign(); i++ {
		for c := 0; c < f.Channels; c++ {
			p := b[44+(i*f.Channels+c)*bps:]
			if bps == 2 {
				out[c] = append(out[c], float64(int16(binary.LittleEndian.Uint16(p)))/32768)
			} else {
				v := int32(p[0]) | int32(p[1])<<8 | int32(int8(p[2]))<<16
				out[c] = append(out[c], float64(v)/8388608)
			}
		}
	}
	return f, out
}

func crossings(s []float64) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return n
}

// makeWAV writes a synthetic WAV. tag is 1 (PCM), 3 (float) or 0xFFFE (extensible PCM).
func makeWAV(t *testing.T, tag, bits, channels, rate, frames int, sample func(frame, ch int) float64) string {
	t.Helper()
	bps := bits / 8
	var data bytes.Buffer
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			v := sample(i, c)
			switch {
			case tag == 3:
				_ = binary.Write(&data, binary.LittleEndian, float32(v))
			case bits == 8:
				data.WriteByte(byte(int(math.Round(v*127)) + 128))
			case bits == 16:
				_ = binary.Write(&data, binary.LittleEndian, int16(math.Round(v*32767)))
			case bits == 24:
				x := int32(math.Round(v * 8388607))
				data.Write([]byte{byte(x), byte(x >> 8), byte(x >> 16)})
			default:
				_ = binary.Write(&data, binary.LittleEndian, int32(math.Round(v*2147483647)))
			}
		}
	}
	var fmtc bytes.Buffer
	le := func(v any) { _ = binary.Write(&fmtc, binary.LittleEndian, v) }
	le(uint16(tag))
	le(uint16(channels))
	le(uint32(rate))
	le(uint32(rate * channels * bps))
	le(uint16(channels * bps))
	le(uint16(bits))
	if tag == 0xFFFE {
		le(uint16(22))
		le(uint16(bits))
		le(uint32(0))
		le(uint16(1)) // sub-format PCM
		fmtc.Write([]byte{0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71})
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+8+fmtc.Len()+8+data.Len()))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(fmtc.Len()))
	out.Write(fmtc.Bytes())
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(data.Len()))
	out.Write(data.Bytes())
	path := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func convert(t *testing.T, src string) (Result, string, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "out.wav.part")
	res, err := Convert(context.Background(), src, dst, Options{}, nil)
	return res, dst, err
}

func TestCompressedFormats(t *testing.T) {
	cases := []struct {
		file  string
		rate  int
		depth int
	}{
		{"tone.mp3", 44100, 16},
		{"tone16.flac", 44100, 16},
		{"tone24.flac", 48000, 24},
		{"tone.ogg", 44100, 16},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			res, dst, err := convert(t, filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatal(err)
			}
			if res.Format.SampleRate != c.rate || res.Format.BitDepth != c.depth || res.Format.Channels != 2 {
				t.Fatalf("format %+v", res.Format)
			}
			if res.DurationMs < 950 || res.DurationMs > 1100 {
				t.Fatalf("duration %d", res.DurationMs)
			}
			_, ch := readPCM(t, dst)
			// Left carries 440 Hz and right 880 Hz, so channels must not be swapped or mixed.
			l, r := crossings(ch[0]), crossings(ch[1])
			if l == 0 || r < l*3/2 {
				t.Fatalf("channel mapping wrong: left %d right %d", l, r)
			}
		})
	}
}

func TestWAVVariants(t *testing.T) {
	sine := func(i, c int) float64 { return 0.5 * math.Sin(float64(i)*0.05*float64(c+1)) }
	cases := []struct {
		name         string
		tag, bits    int
		wantDepth    int
		maxSampleErr float64
	}{
		{"pcm8", 1, 8, 16, 0.01},
		{"pcm16", 1, 16, 16, 0.0001},
		{"pcm24", 1, 24, 24, 0.000001},
		{"pcm32", 1, 32, 24, 0.000001},
		{"float32", 3, 32, 24, 0.000001},
		{"extensible", 0xFFFE, 16, 16, 0.0001},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := makeWAV(t, c.tag, c.bits, 2, 44100, 2000, sine)
			res, dst, err := convert(t, src)
			if err != nil {
				t.Fatal(err)
			}
			if res.Format.BitDepth != c.wantDepth || res.Format.SampleRate != 44100 {
				t.Fatalf("format %+v", res.Format)
			}
			_, ch := readPCM(t, dst)
			if len(ch[0]) != 2000 {
				t.Fatalf("frames %d", len(ch[0]))
			}
			for i := range ch[0] {
				if math.Abs(ch[0][i]-sine(i, 0)) > c.maxSampleErr || math.Abs(ch[1][i]-sine(i, 1)) > c.maxSampleErr {
					t.Fatalf("sample %d: %v/%v want %v/%v", i, ch[0][i], ch[1][i], sine(i, 0), sine(i, 1))
				}
			}
		})
	}
}

func TestMonoStaysMono(t *testing.T) {
	src := makeWAV(t, 1, 16, 1, 22050, 500, func(i, _ int) float64 { return 0.1 })
	res, _, err := convert(t, src)
	if err != nil || res.Format.Channels != 1 || res.Format.SampleRate != 22050 {
		t.Fatalf("%v %+v", err, res.Format)
	}
}

func TestMoreThanTwoChannelsMixDown(t *testing.T) {
	// 6 channels: even ones 0.2, odd ones -0.4.
	src := makeWAV(t, 1, 16, 6, 44100, 100, func(_, c int) float64 {
		if c%2 == 0 {
			return 0.2
		}
		return -0.4
	})
	res, dst, err := convert(t, src)
	if err != nil || res.Format.Channels != 2 {
		t.Fatalf("%v %+v", err, res.Format)
	}
	_, ch := readPCM(t, dst)
	if math.Abs(ch[0][0]-0.2) > 0.001 || math.Abs(ch[1][0]+0.4) > 0.001 {
		t.Fatalf("mix %v %v", ch[0][0], ch[1][0])
	}
}

func TestRejections(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "a.mp3")
	_ = os.WriteFile(text, []byte("this is not audio at all, just text"), 0o644)
	if _, _, err := convert(t, text); err == nil || !strings.Contains(err.Error(), "are supported") {
		t.Fatalf("text: %v", err)
	}
	if _, _, err := convert(t, filepath.Join("testdata", "tone-opus.ogg")); err == nil || !strings.Contains(err.Error(), "Opus") {
		t.Fatalf("opus: %v", err)
	}
	empty := makeWAV(t, 1, 16, 2, 44100, 0, func(int, int) float64 { return 0 })
	if _, _, err := convert(t, empty); err == nil {
		t.Fatal("empty wav accepted")
	}
}

func TestOverLimitRejectedBeforeWriting(t *testing.T) {
	src := makeWAV(t, 1, 16, 2, 44100, 1000, func(int, int) float64 { return 0 })
	dst := filepath.Join(t.TempDir(), "out.part")
	_, err := Convert(context.Background(), src, dst, Options{MaxDataBytes: 1000}, nil)
	if err == nil || !strings.Contains(err.Error(), "4 GiB") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("output left behind")
	}
}

func TestCancelRemovesOutput(t *testing.T) {
	src := makeWAV(t, 1, 16, 2, 44100, 50000, func(int, int) float64 { return 0.1 })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(t.TempDir(), "out.part")
	if _, err := Convert(ctx, src, dst, Options{}, nil); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("output left behind")
	}
}

func TestLongFileStaysWithinMemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	// ~12 minutes of 44.1 kHz stereo 16-bit silence in a sparse file.
	const frames = 32_000_000
	path := filepath.Join(t.TempDir(), "long.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	dataLen := uint32(frames * 4)
	var h bytes.Buffer
	h.WriteString("RIFF")
	_ = binary.Write(&h, binary.LittleEndian, 36+dataLen)
	h.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(2), uint32(44100), uint32(176400), uint16(4), uint16(16)} {
		_ = binary.Write(&h, binary.LittleEndian, v)
	}
	h.WriteString("data")
	_ = binary.Write(&h, binary.LittleEndian, dataLen)
	_, _ = f.Write(h.Bytes())
	_ = f.Truncate(int64(44 + dataLen))
	f.Close()

	runtime.GC()
	var peak uint64
	dst := filepath.Join(t.TempDir(), "out.part")
	_, err = Convert(context.Background(), path, dst, Options{}, func(int64, int64) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapAlloc)
	})
	if err != nil {
		t.Fatal(err)
	}
	if peak > 200<<20 {
		t.Fatalf("peak heap %d MB", peak>>20)
	}
}

func TestErrorsFollowLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale(i18n.English) })
	i18n.SetLocale(i18n.Ukrainian)
	if !strings.Contains(ErrUnsupported.Error(), "підтримуються") {
		t.Fatalf("got %q", ErrUnsupported)
	}
	i18n.SetLocale(i18n.English)
	if !strings.Contains(ErrUnsupported.Error(), "supported") {
		t.Fatalf("got %q", ErrUnsupported)
	}
}
