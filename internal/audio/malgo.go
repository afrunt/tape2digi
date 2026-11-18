package audio

import (
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// MalgoSource implements Source on top of miniaudio.
type MalgoSource struct {
	mu  sync.Mutex
	ctx *malgo.AllocatedContext
}

// NewMalgoSource initialises the platform audio backend.
func NewMalgoSource() (*MalgoSource, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("audio backend: %w", err)
	}
	return &MalgoSource{ctx: ctx}, nil
}

func (s *MalgoSource) CaptureDevices() ([]Device, error) { return s.devices(malgo.Capture) }

func (s *MalgoSource) PlaybackDevices() ([]Device, error) { return s.devices(malgo.Playback) }

func (s *MalgoSource) devices(kind malgo.DeviceType) ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	infos, err := s.ctx.Devices(kind)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(infos))
	for i := range infos {
		out = append(out, Device{
			ID:        infos[i].ID.String(),
			Name:      infos[i].Name(),
			IsDefault: infos[i].IsDefault != 0,
		})
	}
	return out, nil
}

func malgoFormat(bitDepth int) (malgo.FormatType, error) {
	switch bitDepth {
	case 16:
		return malgo.FormatS16, nil
	case 24:
		return malgo.FormatS24, nil
	}
	return 0, fmt.Errorf("unsupported bit depth %d", bitDepth)
}

type malgoStream struct {
	dev    *malgo.Device
	closed sync.Once
}

func (m *malgoStream) Close() error {
	m.closed.Do(func() {
		_ = m.dev.Stop()
		m.dev.Uninit()
	})
	return nil
}

func (s *MalgoSource) OpenCapture(deviceID string, f Format, onFrames func([]byte), onStop func()) (Stream, error) {
	mf, err := malgoFormat(f.BitDepth)
	if err != nil {
		return nil, err
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = mf
	cfg.Capture.Channels = uint32(f.Channels)
	cfg.SampleRate = uint32(f.SampleRate)
	if deviceID != "" {
		id, err := malgo.ParseDeviceID(deviceID)
		if err != nil {
			return nil, err
		}
		cfg.Capture.DeviceID = id.Pointer()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dev, err := malgo.InitDevice(s.ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, _ uint32) { onFrames(in) },
		Stop: onStop,
	})
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, fmt.Errorf("start input: %w", err)
	}
	return &malgoStream{dev: dev}, nil
}

func (s *MalgoSource) OpenPlayback(deviceID string, f Format, fill func(out []byte), onStop func()) (Stream, error) {
	mf, err := malgoFormat(f.BitDepth)
	if err != nil {
		return nil, err
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = mf
	cfg.Playback.Channels = 2
	if deviceID != "" {
		id, err := malgo.ParseDeviceID(deviceID)
		if err != nil {
			return nil, err
		}
		cfg.Playback.DeviceID = id.Pointer()
	}
	cfg.SampleRate = uint32(f.SampleRate)
	cfg.PeriodSizeInMilliseconds = 10
	cfg.Periods = 3
	s.mu.Lock()
	defer s.mu.Unlock()
	dev, err := malgo.InitDevice(s.ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(out, _ []byte, _ uint32) { fill(out) },
		Stop: onStop,
	})
	if err != nil {
		return nil, fmt.Errorf("open output: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, fmt.Errorf("start output: %w", err)
	}
	return &malgoStream{dev: dev}, nil
}

func (s *MalgoSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		_ = s.ctx.Uninit()
		s.ctx.Free()
		s.ctx = nil
	}
}
