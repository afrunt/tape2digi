package audio

import (
	"math"
	"os"
	"sync"
	"time"

	"github.com/afrunt/tape2digi/internal/wav"
)

const playerBufferMs = 300

// PlayerState is a snapshot of file playback.
type PlayerState struct {
	// Active is true while a file is open on an output device, also when paused.
	Active     bool
	Playing    bool
	PositionMs int64
	DurationMs int64
	// Levels is the loudest level per channel played since the previous State
	// call; empty while paused or idle.
	Levels []ChannelLevel
}

// Player plays one WAV file at a time on an output device with seeking. A
// reader goroutine keeps a ring buffer filled so the audio callback never
// touches the disk.
type Player struct {
	src Source

	opMu sync.Mutex
	mu   sync.Mutex
	s    *playback
}

// NewPlayer creates an idle player.
func NewPlayer(src Source) *Player { return &Player{src: src} }

type playback struct {
	file    *os.File
	path    string
	format  Format
	off     int64
	frames  int64
	ring    *Ring
	stream  Stream
	stop    chan struct{}
	stopped sync.Once

	mu      sync.Mutex // guards the fields below
	readPos int64      // next frame the reader loads
	base    int64      // frame the device position is counted from
	played  int64      // frames handed to the device since base
	paused  bool
	eof     bool
	levels  []ChannelLevel
}

// Start opens path on the output device (empty: system default) and plays it
// from fromMs, replacing any current playback.
func (p *Player) Start(path, deviceID string, fromMs int64) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopLocked()

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	format, off, length, err := wav.ReadHeader(f, st.Size())
	if err != nil {
		_ = f.Close()
		return err
	}
	frame := format.BlockAlign()
	pb := &playback{
		file: f, path: path, format: format, off: off, frames: length / int64(frame),
		ring: NewRing(format.SampleRate*playerBufferMs/1000*frame, frame), stop: make(chan struct{}),
	}
	pos := fromMs * int64(format.SampleRate) / 1000
	if pos < 0 || pos > pb.frames {
		pos = 0
	}
	pb.readPos, pb.base = pos, pos
	stream, err := p.src.OpenPlayback(deviceID, format, pb.filler(), func() { go p.stopIf(pb) })
	if err != nil {
		_ = f.Close()
		return err
	}
	pb.stream = stream
	p.mu.Lock()
	p.s = pb
	p.mu.Unlock()
	go pb.read(p)
	return nil
}

// Path returns the file being played, or "" when idle.
func (p *Player) Path() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.s == nil {
		return ""
	}
	return p.s.path
}

func (p *Player) current() *playback {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.s
}

// Pause holds the position; Resume continues.
func (p *Player) Pause()  { p.setPaused(true) }
func (p *Player) Resume() { p.setPaused(false) }

func (p *Player) setPaused(v bool) {
	if s := p.current(); s != nil {
		s.mu.Lock()
		s.paused = v
		s.mu.Unlock()
	}
}

// SeekTo jumps to ms and drops buffered audio.
func (p *Player) SeekTo(ms int64) {
	s := p.current()
	if s == nil {
		return
	}
	pos := ms * int64(s.format.SampleRate) / 1000
	if pos < 0 {
		pos = 0
	}
	if pos > s.frames {
		pos = s.frames
	}
	s.mu.Lock()
	s.readPos, s.base, s.played, s.eof = pos, pos, 0, false
	s.ring.Reset()
	s.mu.Unlock()
}

// Stop ends playback and closes the file and the device.
func (p *Player) Stop() {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopLocked()
}

func (p *Player) stopIf(s *playback) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if p.current() == s {
		p.stopLocked()
	}
}

func (p *Player) stopLocked() {
	p.mu.Lock()
	s := p.s
	p.s = nil
	p.mu.Unlock()
	if s == nil {
		return
	}
	s.stopped.Do(func() { close(s.stop) })
	if s.stream != nil {
		_ = s.stream.Close()
	}
	_ = s.file.Close()
}

// State reports the current playback.
func (p *Player) State() PlayerState {
	s := p.current()
	if s == nil {
		return PlayerState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rate := int64(s.format.SampleRate)
	lv := s.levels
	s.levels = nil
	return PlayerState{
		Active: true, Playing: !s.paused,
		PositionMs: (s.base + s.played) * 1000 / rate, DurationMs: s.frames * 1000 / rate,
		Levels: lv,
	}
}

// filler is the audio callback: it only reads the ring and counts frames.
func (s *playback) filler() func(out []byte) {
	var tmp []byte
	f := s.format
	bps := f.BitDepth / 8
	return func(out []byte) {
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if paused {
			clear(out)
			return
		}
		var n int
		if f.Channels == 2 {
			n = s.ring.Read(out)
			s.addLevels(out[:n])
		} else {
			monoBytes := len(out) / 2
			if cap(tmp) < monoBytes {
				tmp = make([]byte, monoBytes)
			}
			mono := tmp[:monoBytes]
			n = s.ring.Read(mono)
			s.addLevels(mono[:n])
			for i := 0; i+bps <= monoBytes; i += bps {
				copy(out[2*i:], mono[i:i+bps])
				copy(out[2*i+bps:], mono[i:i+bps])
			}
		}
		s.mu.Lock()
		s.played += int64(n / f.BlockAlign())
		s.mu.Unlock()
	}
}

// addLevels folds the played audio into the maximum since the last State.
func (s *playback) addLevels(pcm []byte) {
	if len(pcm) < s.format.BlockAlign() {
		return
	}
	l := Levels(pcm, s.format)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.levels == nil {
		s.levels = l
		return
	}
	for i := range l {
		s.levels[i].PeakDB = math.Max(s.levels[i].PeakDB, l[i].PeakDB)
		s.levels[i].RMSDB = math.Max(s.levels[i].RMSDB, l[i].RMSDB)
		s.levels[i].Clip = s.levels[i].Clip || l[i].Clip
	}
}

// read keeps the ring filled and ends playback once the file is drained.
func (s *playback) read(p *Player) {
	frame := int64(s.format.BlockAlign())
	buf := make([]byte, 16384-16384%int(frame))
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		s.mu.Lock()
		pos, eof := s.readPos, s.eof
		s.mu.Unlock()
		if eof {
			if s.ring.Len() == 0 {
				go p.stopIf(s)
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		n := int64(len(buf)) / frame
		if room := int64(s.ring.Free()) / frame; room < n {
			n = room
		}
		if n == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if n > s.frames-pos {
			n = s.frames - pos
		}
		var got int
		if n > 0 {
			got, _ = s.file.ReadAt(buf[:n*frame], s.off+pos*frame)
		}
		s.mu.Lock()
		if s.readPos == pos && !s.eof {
			if got > 0 {
				s.ring.Write(buf[:got-got%int(frame)])
				s.readPos = pos + int64(got)/frame
			}
			if n == 0 || got < int(n*frame) {
				s.eof = true
			}
		}
		s.mu.Unlock()
	}
}
