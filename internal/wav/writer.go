// Package wav implements an incremental PCM WAV writer whose header is
// patched periodically so that an interrupted recording stays playable.
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	headerSize = 44
	// MaxDataBytes is the largest data chunk a RIFF/WAV file can describe.
	MaxDataBytes = uint64(0xFFFFFFFF) - (headerSize - 8)
)

// ErrSizeLimit is returned when a write would exceed the WAV size limit.
var ErrSizeLimit = errors.New("wav: file size limit reached")

// Format describes the PCM layout.
type Format struct {
	SampleRate int
	Channels   int
	BitDepth   int // 16 or 24
}

// BlockAlign is the number of bytes in one frame.
func (f Format) BlockAlign() int { return f.Channels * f.BitDepth / 8 }

// Validate checks that the format can be written.
func (f Format) Validate() error {
	if f.SampleRate <= 0 {
		return fmt.Errorf("wav: invalid sample rate %d", f.SampleRate)
	}
	if f.Channels < 1 || f.Channels > 2 {
		return fmt.Errorf("wav: unsupported channel count %d", f.Channels)
	}
	if f.BitDepth != 16 && f.BitDepth != 24 {
		return fmt.Errorf("wav: unsupported bit depth %d", f.BitDepth)
	}
	return nil
}

// File is the storage a Writer needs; *os.File satisfies it.
type File interface {
	io.WriterAt
	Sync() error
}

// Writer streams PCM data into a WAV file.
type Writer struct {
	f            File
	format       Format
	dataBytes    uint64
	patchedAt    uint64
	patchEvery   uint64
	maxData      uint64
	closed       bool
	limitReached bool
}

// Option customises a Writer.
type Option func(*Writer)

// WithMaxDataBytes lowers the data size limit (used by tests).
func WithMaxDataBytes(n uint64) Option { return func(w *Writer) { w.maxData = n } }

// WithPatchInterval sets how many data bytes pass between header patches.
func WithPatchInterval(n uint64) Option { return func(w *Writer) { w.patchEvery = n } }

// NewWriter writes the initial header and returns a Writer. By default the
// header is refreshed about every two seconds of audio.
func NewWriter(f File, format Format, opts ...Option) (*Writer, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	w := &Writer{
		f:          f,
		format:     format,
		maxData:    MaxDataBytes,
		patchEvery: uint64(format.SampleRate * format.BlockAlign() * 2),
	}
	for _, o := range opts {
		o(w)
	}
	w.maxData -= w.maxData % uint64(format.BlockAlign())
	if err := w.writeHeader(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write appends PCM bytes. When the size limit is hit it writes what fits,
// finalises the header and returns ErrSizeLimit.
func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("wav: write after close")
	}
	if w.limitReached {
		return 0, ErrSizeLimit
	}
	var limitErr error
	if room := w.maxData - w.dataBytes; uint64(len(p)) > room {
		p = p[:room]
		limitErr = ErrSizeLimit
		w.limitReached = true
	}
	n, err := w.f.WriteAt(p, int64(headerSize+w.dataBytes))
	w.dataBytes += uint64(n)
	if err != nil {
		return n, err
	}
	if limitErr != nil || w.dataBytes-w.patchedAt >= w.patchEvery {
		if perr := w.patch(); perr != nil {
			return n, perr
		}
	}
	return n, limitErr
}

// DataBytes returns the number of PCM bytes written so far.
func (w *Writer) DataBytes() uint64 { return w.dataBytes }

// Close finalises the header and syncs the file. It does not close the file.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.patch(); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *Writer) header() []byte {
	b := make([]byte, headerSize)
	f := w.format
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(w.dataBytes+headerSize-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(f.Channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(f.SampleRate))
	binary.LittleEndian.PutUint32(b[28:], uint32(f.SampleRate*f.BlockAlign()))
	binary.LittleEndian.PutUint16(b[32:], uint16(f.BlockAlign()))
	binary.LittleEndian.PutUint16(b[34:], uint16(f.BitDepth))
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(w.dataBytes))
	return b
}

func (w *Writer) writeHeader() error {
	_, err := w.f.WriteAt(w.header(), 0)
	return err
}

func (w *Writer) patch() error {
	if err := w.writeHeader(); err != nil {
		return err
	}
	w.patchedAt = w.dataBytes
	return nil
}

// Inspect reads the format from the header of a WAV written by Writer and
// derives the data size from the file size, so it also works for a file whose
// header was not finalised (an interrupted take).
func Inspect(f io.ReaderAt, size int64) (Format, uint64, error) {
	b := make([]byte, headerSize)
	if _, err := f.ReadAt(b, 0); err != nil {
		return Format{}, 0, errors.New("wav: file is too short")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" ||
		binary.LittleEndian.Uint16(b[20:]) != 1 {
		return Format{}, 0, errors.New("wav: unsupported header")
	}
	format := Format{
		Channels:   int(binary.LittleEndian.Uint16(b[22:])),
		SampleRate: int(binary.LittleEndian.Uint32(b[24:])),
		BitDepth:   int(binary.LittleEndian.Uint16(b[34:])),
	}
	if err := format.Validate(); err != nil {
		return Format{}, 0, err
	}
	data := uint64(size - headerSize)
	if size < headerSize {
		data = 0
	}
	data -= data % uint64(format.BlockAlign())
	return format, data, nil
}

// Repair rewrites the header sizes of an interrupted take so that they match
// the data actually on disk, and returns the format and data size.
func Repair(f interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
}, size int64) (Format, uint64, error) {
	format, data, err := Inspect(f, size)
	if err != nil {
		return Format{}, 0, err
	}
	w := &Writer{f: f, format: format, dataBytes: data}
	if err := w.patch(); err != nil {
		return Format{}, 0, err
	}
	return format, data, f.Sync()
}
