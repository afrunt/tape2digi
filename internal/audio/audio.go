// Package audio holds device enumeration, capture, level metering and the
// monitor playback path. Hardware access sits behind the Source interface so
// the rest of the app can be tested with a fake.
package audio

import "github.com/afrunt/tape2digi/internal/wav"

// Format is the PCM layout of a stream (16 or 24-bit little-endian, 1-2 channels).
type Format = wav.Format

// Device is an audio endpoint. ID is opaque and may change between runs, so
// callers persist Name and re-resolve the ID.
type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// Stream is a running capture or playback stream.
type Stream interface {
	Close() error
}

// Source provides access to the audio hardware.
type Source interface {
	// CaptureDevices lists input devices.
	CaptureDevices() ([]Device, error)
	// OpenCapture starts capturing from the device. onFrames is called from
	// the audio thread with a slice that is only valid during the call and
	// must not block. onStop is called when the device stops unexpectedly.
	OpenCapture(deviceID string, f Format, onFrames func([]byte), onStop func()) (Stream, error)
	// PlaybackDevices lists output devices.
	PlaybackDevices() ([]Device, error)
	// OpenPlayback starts a playback stream on the given output device, or on
	// the system default output when deviceID is empty, with the given rate
	// and bit depth and always two channels. fill is called from the audio
	// thread and must write exactly len(out) bytes. onStop is called when the
	// device stops unexpectedly.
	OpenPlayback(deviceID string, f Format, fill func(out []byte), onStop func()) (Stream, error)
	// Close releases the audio backend.
	Close()
}
