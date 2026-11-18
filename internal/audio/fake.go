package audio

import (
	"errors"
	"sync"
)

// FakeSource is an in-memory Source for tests: frames are injected with
// Emit and playback output is pulled with Pull.
type FakeSource struct {
	mu          sync.Mutex
	Devs        []Device
	OpenErr     error
	PlaybackErr error
	// PlayDevs is returned by PlaybackDevices.
	PlayDevs []Device
	// PlayGate, when set, blocks OpenPlayback until it is closed.
	PlayGate chan struct{}
	// StopOnClose makes closing a playback stream invoke its onStop, as some
	// backends do.
	StopOnClose bool
	// LastPlayDevice is the device ID of the latest OpenPlayback call.
	LastPlayDevice string
	playGen        int
	capture        func([]byte)
	captureStop    func()
	fill           func([]byte)
	onStop         func()
	CaptureOpen    bool
	PlayOpen       bool
	LastDevice     string
	LastFormat     Format
}

type fakeStream struct{ close func() }

func (s fakeStream) Close() error { s.close(); return nil }

func (f *FakeSource) CaptureDevices() ([]Device, error) { return f.Devs, nil }

func (f *FakeSource) OpenCapture(id string, fm Format, on func([]byte), onStop func()) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.OpenErr != nil {
		return nil, f.OpenErr
	}
	f.capture, f.captureStop, f.CaptureOpen, f.LastDevice, f.LastFormat = on, onStop, true, id, fm
	return fakeStream{func() {
		f.mu.Lock()
		f.capture, f.CaptureOpen = nil, false
		f.mu.Unlock()
	}}, nil
}

func (f *FakeSource) PlaybackDevices() ([]Device, error) { return f.PlayDevs, nil }

func (f *FakeSource) OpenPlayback(id string, _ Format, fill func([]byte), onStop func()) (Stream, error) {
	f.mu.Lock()
	gate := f.PlayGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	if f.PlaybackErr != nil {
		f.mu.Unlock()
		return nil, f.PlaybackErr
	}
	f.playGen++
	gen := f.playGen
	f.fill, f.onStop, f.PlayOpen, f.LastPlayDevice = fill, onStop, true, id
	f.mu.Unlock()
	return fakeStream{func() {
		f.mu.Lock()
		if f.playGen == gen {
			f.fill, f.PlayOpen = nil, false
		}
		stopOnClose := f.StopOnClose
		f.mu.Unlock()
		if stopOnClose && onStop != nil {
			onStop()
		}
	}}, nil
}

func (f *FakeSource) Close() {}

// Emit delivers frames as if captured.
func (f *FakeSource) Emit(p []byte) error {
	f.mu.Lock()
	c := f.capture
	f.mu.Unlock()
	if c == nil {
		return errors.New("capture not open")
	}
	c(p)
	return nil
}

// Pull reads n bytes of playback output.
func (f *FakeSource) Pull(n int) ([]byte, error) {
	f.mu.Lock()
	fill := f.fill
	f.mu.Unlock()
	if fill == nil {
		return nil, errors.New("playback not open")
	}
	out := make([]byte, n)
	fill(out)
	return out, nil
}

// StopPlayback simulates the output device being lost.
func (f *FakeSource) StopPlayback() {
	f.mu.Lock()
	cb := f.onStop
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// StopCapture simulates the input device disappearing.
func (f *FakeSource) StopCapture() {
	f.mu.Lock()
	cb := f.captureStop
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}
