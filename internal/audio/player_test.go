package audio

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afrunt/tape2digi/internal/wav"
)

func writeTestWAV(t *testing.T, f Format, frames int, sample func(i int) byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.wav")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(file, f)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, frames*f.BlockAlign())
	for i := range b {
		b[i] = sample(i / f.BlockAlign())
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	return path
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestPlayerPlaysSeeksPausesAndEnds(t *testing.T) {
	f := Format{SampleRate: 1000, Channels: 2, BitDepth: 16}
	path := writeTestWAV(t, f, 1000, func(i int) byte { return byte(i / 10) })
	src := &FakeSource{}
	p := NewPlayer(src)
	if err := p.Start(path, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return p.current().ring.Len() >= 40 })
	out, _ := src.Pull(40) // 10 frames
	if out[0] != 0 || out[36] != 0 {
		t.Fatalf("unexpected samples % x", out)
	}
	if st := p.State(); !st.Playing || st.PositionMs != 10 || st.DurationMs != 1000 {
		t.Fatalf("state %+v", st)
	}

	p.SeekTo(500)
	waitFor(t, func() bool { return p.current().ring.Len() >= 4 })
	out, _ = src.Pull(4)
	if out[0] != 50 {
		t.Fatalf("after seek got %d", out[0])
	}
	p.Pause()
	before := p.State().PositionMs
	if out, _ = src.Pull(40); out[0] != 0 || p.State().PositionMs != before || p.State().Playing {
		t.Fatal("pause must hold position and output silence")
	}
	p.Resume()
	p.SeekTo(990)
	for i := 0; i < 200 && p.State().Active; i++ {
		_, _ = src.Pull(40)
		time.Sleep(5 * time.Millisecond)
	}
	if p.State().Active {
		t.Fatal("playback must stop at the end of the file")
	}
}

func TestPlayerMono24DuplicatesAndReleasesFile(t *testing.T) {
	f := Format{SampleRate: 1000, Channels: 1, BitDepth: 24}
	path := writeTestWAV(t, f, 100, func(i int) byte { return byte(i + 1) })
	src := &FakeSource{}
	p := NewPlayer(src)
	if err := p.Start(path, "dev", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return p.current().ring.Len() >= 6 })
	out, _ := src.Pull(12) // two stereo frames
	if string(out[:3]) != string(out[3:6]) || out[0] == 0 {
		t.Fatalf("mono must be duplicated: % x", out)
	}
	if src.LastPlayDevice != "dev" {
		t.Fatal("device not used")
	}
	file := p.current().file
	p.Stop()
	if _, err := file.Stat(); err == nil {
		t.Fatal("file must be closed after stop")
	}
	if p.State().Active || p.Path() != "" {
		t.Fatal("must be idle")
	}
}

func TestPlayerReportsLevelsSincePreviousRead(t *testing.T) {
	f := Format{SampleRate: 1000, Channels: 2, BitDepth: 16}
	// left channel at half scale, right channel silent
	path := writeTestWAV(t, f, 1000, func(i int) byte { return 0 })
	file, _ := os.OpenFile(path, os.O_RDWR, 0)
	_, off, _, _ := wav.ReadHeader(file, mustSize(t, file))
	for i := int64(0); i < 1000; i++ {
		_, _ = file.WriteAt([]byte{0, 0x40}, off+i*4)
	}
	_ = file.Close()
	src := &FakeSource{}
	p := NewPlayer(src)
	if err := p.Start(path, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return p.current().ring.Len() >= 40 })
	_, _ = src.Pull(40)
	lv := p.State().Levels
	if len(lv) != 2 || lv[0].PeakDB < -6.1 || lv[0].PeakDB > -5.9 || lv[1].PeakDB > -100 {
		t.Fatalf("levels %+v", lv)
	}
	if got := p.State().Levels; got != nil {
		t.Fatalf("a read must reset the levels, got %+v", got)
	}
	p.Stop()
}

func mustSize(t *testing.T, f *os.File) int64 {
	t.Helper()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
