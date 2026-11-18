package audio

import "sync"

// Monitor plays captured audio on an output device through its own ring
// buffer. It is fully isolated from recording: Feed never blocks and every
// failure is reported to the caller instead of propagating to capture.
type Monitor struct {
	src    Source
	format Format

	// opMu serialises Start, Restart and Stop; the device open runs under it
	// but never under mu, so Feed and Active stay responsive.
	opMu sync.Mutex
	mu   sync.Mutex
	ring *Ring
	// gen identifies the current stream so a stop reported by an old or
	// closed stream is not mistaken for losing the current device.
	gen    uint64
	stream Stream
}

// monitorBufferMs bounds the added latency.
const monitorBufferMs = 120

// NewMonitor creates a stopped monitor for the given input format.
func NewMonitor(src Source, f Format) *Monitor { return &Monitor{src: src, format: f} }

// Start opens the output device (empty ID: system default). It does nothing
// while already playing. onStop is invoked if the current stream stops
// unexpectedly.
func (m *Monitor) Start(deviceID string, onStop func()) error {
	return m.swap(deviceID, onStop, false)
}

// Restart moves playback to another output device. When the new device cannot
// be opened the monitor ends up stopped and the error is returned.
func (m *Monitor) Restart(deviceID string, onStop func()) error {
	return m.swap(deviceID, onStop, true)
}

func (m *Monitor) swap(deviceID string, onStop func(), force bool) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.stream != nil && !force {
		m.mu.Unlock()
		return nil
	}
	m.gen++
	gen := m.gen
	m.mu.Unlock()

	frame := m.format.BlockAlign()
	ring := NewRing(m.format.SampleRate*monitorBufferMs/1000*frame, frame)
	stream, err := m.src.OpenPlayback(deviceID, m.format, m.filler(ring), func() { m.streamStopped(gen, onStop) })

	m.mu.Lock()
	old := m.stream
	if err != nil {
		m.stream, m.ring = nil, nil
		m.gen++
	} else {
		m.stream, m.ring = stream, ring
	}
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return err
}

// Stop closes the output device.
func (m *Monitor) Stop() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	stream := m.stream
	m.stream, m.ring = nil, nil
	m.gen++
	m.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// streamStopped forwards a stop of the current stream. The callback runs on
// its own goroutine because it usually calls Stop, which a device thread must
// not wait for while a Restart is closing that very stream.
func (m *Monitor) streamStopped(gen uint64, onStop func()) {
	m.mu.Lock()
	current := m.gen == gen && m.stream != nil
	m.mu.Unlock()
	if current && onStop != nil {
		go onStop()
	}
}

// Active reports whether playback is running.
func (m *Monitor) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stream != nil
}

// Feed queues captured frames for playback; it is safe to call from the audio thread.
func (m *Monitor) Feed(p []byte) {
	if !m.mu.TryLock() {
		return
	}
	ring := m.ring
	m.mu.Unlock()
	if ring != nil {
		ring.Write(p)
	}
}

// Stats returns ring overrun bytes and underrun count of the active session.
func (m *Monitor) Stats() (overruns, underruns uint64) {
	m.mu.Lock()
	ring := m.ring
	m.mu.Unlock()
	if ring == nil {
		return 0, 0
	}
	return ring.Stats()
}

// filler returns the stream callback that converts the (mono or stereo) ring
// content into stereo output. Each stream owns its mono scratch buffer.
func (m *Monitor) filler(ring *Ring) func(out []byte) {
	var tmp []byte
	f := m.format
	return func(out []byte) {
		if f.Channels == 2 {
			ring.Read(out)
			return
		}
		bps := f.BitDepth / 8
		monoBytes := len(out) / 2
		if cap(tmp) < monoBytes {
			tmp = make([]byte, monoBytes)
		}
		mono := tmp[:monoBytes]
		ring.Read(mono)
		for i := 0; i+bps <= monoBytes; i += bps {
			copy(out[2*i:], mono[i:i+bps])
			copy(out[2*i+bps:], mono[i:i+bps])
		}
	}
}
