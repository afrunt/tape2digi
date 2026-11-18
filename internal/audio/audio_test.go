package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func sine16(frames, channels int, amp float64) []byte {
	b := make([]byte, 0, frames*channels*2)
	for i := 0; i < frames; i++ {
		v := int16(amp * math.Sin(2*math.Pi*float64(i)/48))
		for c := 0; c < channels; c++ {
			b = binary.LittleEndian.AppendUint16(b, uint16(v))
		}
	}
	return b
}

func TestLevelsStereoChannelsIndependent(t *testing.T) {
	f := Format{SampleRate: 48000, Channels: 2, BitDepth: 16}
	buf := make([]byte, 0, 4*1000)
	loud := sine16(1000, 1, 16384)
	for i := 0; i < 1000; i++ {
		buf = append(buf, loud[2*i], loud[2*i+1], 0, 0) // L loud, R silent
	}
	lv := Levels(buf, f)
	if len(lv) != 2 {
		t.Fatalf("channels = %d", len(lv))
	}
	if math.Abs(lv[0].PeakDB-(-6.02)) > 0.1 {
		t.Fatalf("L peak %v", lv[0].PeakDB)
	}
	if lv[1].PeakDB != SilenceDB || lv[1].Clip {
		t.Fatalf("R %+v", lv[1])
	}
}

func TestLevelsMonoAndClip(t *testing.T) {
	f := Format{SampleRate: 48000, Channels: 1, BitDepth: 16}
	lv := Levels([]byte{0xFF, 0x7F, 0, 0}, f)
	if len(lv) != 1 || !lv[0].Clip || lv[0].PeakDB < -0.01 {
		t.Fatalf("%+v", lv)
	}
}

func TestLevels24Bit(t *testing.T) {
	f := Format{SampleRate: 48000, Channels: 1, BitDepth: 24}
	half := []byte{0x00, 0x00, 0x40} // 0x400000 = half scale
	lv := Levels(half, f)
	if math.Abs(lv[0].PeakDB-(-6.02)) > 0.1 || lv[0].Clip {
		t.Fatalf("%+v", lv[0])
	}
	neg := []byte{0x00, 0x00, 0x80} // -full scale
	if !Levels(neg, f)[0].Clip {
		t.Fatal("expected clip on -full scale")
	}
}

func TestLevelsEmpty(t *testing.T) {
	lv := Levels(nil, Format{SampleRate: 48000, Channels: 2, BitDepth: 16})
	if len(lv) != 2 || lv[0].PeakDB != SilenceDB {
		t.Fatalf("%+v", lv)
	}
}

func TestQueueNeverBlocksAndCountsOverruns(t *testing.T) {
	q := NewQueue(2)
	for i := 0; i < 5; i++ {
		q.Push([]byte{byte(i)})
	}
	if q.Overruns() != 3 {
		t.Fatalf("overruns %d", q.Overruns())
	}
	q.Close()
	var got []byte
	for c := range q.Chunks() {
		got = append(got, c...)
	}
	if !bytes.Equal(got, []byte{0, 1}) {
		t.Fatalf("got %v", got)
	}
}

func TestRingUnderrunPadsSilenceAndOverrunDropsOldest(t *testing.T) {
	r := NewRing(8, 2)
	r.Write([]byte{1, 1, 2, 2})
	out := make([]byte, 8)
	if n := r.Read(out); n != 4 || !bytes.Equal(out, []byte{1, 1, 2, 2, 0, 0, 0, 0}) {
		t.Fatalf("n=%d out=%v", n, out)
	}
	if _, u := r.Stats(); u != 1 {
		t.Fatalf("underruns %d", u)
	}
	r.Write([]byte{1, 1, 2, 2, 3, 3, 4, 4})
	r.Write([]byte{5, 5})
	out = make([]byte, 8)
	r.Read(out)
	if !bytes.Equal(out, []byte{2, 2, 3, 3, 4, 4, 5, 5}) {
		t.Fatalf("out=%v", out)
	}
	if o, _ := r.Stats(); o != 2 {
		t.Fatalf("overruns %d", o)
	}
}

func TestMonitorMonoDuplicatedToStereo(t *testing.T) {
	src := &FakeSource{}
	m := NewMonitor(src, Format{SampleRate: 48000, Channels: 1, BitDepth: 16})
	if err := m.Start("", nil); err != nil {
		t.Fatal(err)
	}
	m.Feed([]byte{1, 2, 3, 4})
	out, _ := src.Pull(8)
	if !bytes.Equal(out, []byte{1, 2, 1, 2, 3, 4, 3, 4}) {
		t.Fatalf("out=%v", out)
	}
	m.Stop()
	if src.PlayOpen || m.Active() {
		t.Fatal("playback still open")
	}
}

func TestMonitorStartFailureIsReportedNotFatal(t *testing.T) {
	src := &FakeSource{PlaybackErr: errTest}
	m := NewMonitor(src, Format{SampleRate: 48000, Channels: 2, BitDepth: 16})
	if err := m.Start("", nil); err == nil {
		t.Fatal("expected error")
	}
	m.Feed([]byte{1, 2, 3, 4}) // must be a harmless no-op
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("no output device")
