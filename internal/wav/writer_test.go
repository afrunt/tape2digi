package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gowav "github.com/go-audio/wav"
)

func pcm(frames, channels, bytesPerSample int) []byte {
	b := make([]byte, frames*channels*bytesPerSample)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	for _, depth := range []int{16, 24} {
		for _, ch := range []int{1, 2} {
			path := filepath.Join(t.TempDir(), "a.wav")
			f, _ := os.Create(path)
			w, err := NewWriter(f, Format{SampleRate: 48000, Channels: ch, BitDepth: depth})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(pcm(4800, ch, depth/8)); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			f.Close()

			rf, _ := os.Open(path)
			d := gowav.NewDecoder(rf)
			if !d.IsValidFile() {
				t.Fatalf("depth %d ch %d: invalid wav", depth, ch)
			}
			d.ReadInfo()
			if int(d.BitDepth) != depth || int(d.NumChans) != ch || int(d.SampleRate) != 48000 {
				t.Fatalf("bad format %+v", d)
			}
			dur, _ := d.Duration()
			if dur.Milliseconds() != 100 {
				t.Fatalf("duration %v", dur)
			}
			rf.Close()
		}
	}
}

func TestHeaderPatchedPeriodicallyForCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.wav")
	f, _ := os.Create(path)
	w, _ := NewWriter(f, Format{SampleRate: 1000, Channels: 1, BitDepth: 16}, WithPatchInterval(2000))
	chunk := pcm(500, 1, 2) // 1000 bytes
	for i := 0; i < 5; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	// no Close: simulate the app being killed
	raw, _ := os.ReadFile(path)
	if got := binary.LittleEndian.Uint32(raw[40:]); got != 4000 {
		t.Fatalf("data size in header = %d, want last patch 4000", got)
	}
	if len(raw) != headerSize+5000 {
		t.Fatalf("file size %d", len(raw))
	}
	if !gowav.NewDecoder(bytes.NewReader(raw)).IsValidFile() {
		t.Fatal("interrupted file not decodable")
	}
}

func TestSizeLimitStopsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.wav")
	f, _ := os.Create(path)
	w, _ := NewWriter(f, Format{SampleRate: 1000, Channels: 2, BitDepth: 16}, WithMaxDataBytes(1002))
	n, err := w.Write(pcm(500, 2, 2)) // 2000 bytes
	if !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("err = %v", err)
	}
	if n != 1000 {
		t.Fatalf("n = %d", n)
	}
	if _, err := w.Write([]byte{1, 2, 3, 4}); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("second write err = %v", err)
	}
	w.Close()
	raw, _ := os.ReadFile(path)
	if binary.LittleEndian.Uint32(raw[40:]) != 1000 || len(raw) != headerSize+1000 {
		t.Fatalf("finalised file wrong: header %d len %d", binary.LittleEndian.Uint32(raw[40:]), len(raw))
	}
}

func TestInvalidFormat(t *testing.T) {
	if _, err := NewWriter(nil, Format{SampleRate: 48000, Channels: 3, BitDepth: 16}); err == nil {
		t.Fatal("expected error")
	}
}
