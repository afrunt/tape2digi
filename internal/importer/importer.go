// Package importer converts an existing audio file (wav, mp3, flac, ogg
// vorbis) into a project-ready WAV without loading it into memory.
package importer

import (
	"bufio"
	"context"
	"errors"
	"github.com/afrunt/tape2digi/internal/i18n"
	"io"
	"math"
	"os"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"

	"github.com/afrunt/tape2digi/internal/wav"
)

const (
	chunkFrames   = 4096
	progressEvery = 250 * time.Millisecond
)

// ErrUnsupported marks a file that is not a supported audio format.
var ErrUnsupported = i18n.New("importer.unsupportedFileFormatWavMp3")

type info struct {
	Rate, Channels, Bits int
	Float                bool
	Frames               int64 // -1 when unknown
}

type decoder interface {
	Info() info
	// Next fills dst with interleaved samples in [-1,1] and returns how many
	// values were written (a multiple of the channel count), or io.EOF.
	Next(dst []float32) (int, error)
}

// Options tune a conversion.
type Options struct {
	// MaxDataBytes lowers the WAV size cap; zero means the format maximum.
	MaxDataBytes uint64
}

// Result describes the converted file.
type Result struct {
	Format     wav.Format
	DataBytes  uint64
	DurationMs int64
}

// Convert decodes src and writes a WAV to dst, which must not exist. Output
// is 16-bit, or 24-bit when the source has at least 24 bits or is float; the
// sample rate is kept and sources with more than two channels are mixed down
// to stereo. Progress receives bytes written and the expected total (zero when
// unknown). On any error or cancellation dst is removed.
func Convert(ctx context.Context, src, dst string, opts Options, progress func(done, total int64)) (res Result, err error) {
	in, err := os.Open(src)
	if err != nil {
		return Result{}, err
	}
	defer in.Close()
	dec, err := newDecoder(in)
	if err != nil {
		return Result{}, err
	}
	si := dec.Info()

	f := wav.Format{SampleRate: si.Rate, Channels: min(si.Channels, 2), BitDepth: 16}
	if si.Bits >= 24 || si.Float {
		f.BitDepth = 24
	}
	if err := f.Validate(); err != nil {
		return Result{}, i18n.New("importer.unsupportedSourceFormat", err)
	}
	limit := uint64(wav.MaxDataBytes)
	if opts.MaxDataBytes > 0 {
		limit = opts.MaxDataBytes
	}
	var total int64
	if si.Frames > 0 {
		total = si.Frames * int64(f.BlockAlign())
		if uint64(total) > limit {
			return Result{}, i18n.New("importer.theResultExceedsTheMaximum")
		}
	}

	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err != nil {
			out.Close()
			_ = os.Remove(dst)
		}
	}()
	var wopts []wav.Option
	if opts.MaxDataBytes > 0 {
		wopts = append(wopts, wav.WithMaxDataBytes(opts.MaxDataBytes))
	}
	w, err := wav.NewWriter(out, f, wopts...)
	if err != nil {
		return Result{}, err
	}

	var (
		in32  = make([]float32, chunkFrames*si.Channels)
		pcm   = make([]byte, 0, chunkFrames*f.BlockAlign())
		last  = time.Now()
		bytes int64
	)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		n, derr := dec.Next(in32)
		if n > 0 {
			pcm = encode(pcm[:0], in32[:n], si.Channels, f)
			if _, err := w.Write(pcm); err != nil {
				if errors.Is(err, wav.ErrSizeLimit) {
					return Result{}, i18n.New("importer.theResultExceedsTheMaximum")
				}
				return Result{}, err
			}
			bytes += int64(len(pcm))
			if progress != nil && time.Since(last) >= progressEvery {
				last = time.Now()
				progress(bytes, total)
			}
		}
		if derr == io.EOF {
			break
		}
		if derr != nil {
			return Result{}, i18n.New("importer.couldNotDecodeTheFile", derr)
		}
	}
	size := w.DataBytes()
	if size == 0 {
		return Result{}, i18n.New("importer.theFileContainsNoAudio")
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}
	if err := out.Close(); err != nil {
		return Result{}, err
	}
	if progress != nil {
		progress(int64(size), int64(size))
	}
	return Result{Format: f, DataBytes: size, DurationMs: int64(size * 1000 / uint64(f.SampleRate*f.BlockAlign()))}, nil
}

// encode converts interleaved float samples into little-endian PCM frames.
func encode(dst []byte, in []float32, srcCh int, f wav.Format) []byte {
	scale := float64(32768)
	if f.BitDepth == 24 {
		scale = 8388608
	}
	put := func(v float64) {
		x := int32(math.Round(v * scale))
		x = max(int32(-scale), min(int32(scale)-1, x))
		dst = append(dst, byte(x), byte(x>>8))
		if f.BitDepth == 24 {
			dst = append(dst, byte(x>>16))
		}
	}
	for i := 0; i+srcCh <= len(in); i += srcCh {
		fr := in[i : i+srcCh]
		switch {
		case srcCh <= 2:
			for _, v := range fr {
				put(float64(v))
			}
		default:
			// Even channels go to the left, odd to the right.
			var l, r float64
			var nl, nr float64
			for c, v := range fr {
				if c%2 == 0 {
					l += float64(v)
					nl++
				} else {
					r += float64(v)
					nr++
				}
			}
			put(l / nl)
			put(r / nr)
		}
	}
	return dst
}

func newDecoder(f *os.File) (decoder, error) {
	head := make([]byte, 64)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	if len(head) < 4 {
		return nil, ErrUnsupported
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	switch {
	case string(head[0:4]) == "RIFF":
		return newWAVDecoder(f)
	case string(head[0:4]) == "fLaC", hasID3(head) && afterID3(f, head) == "fLaC":
		return newFLACDecoder(f)
	case string(head[0:4]) == "OggS":
		if len(head) >= 36 && string(head[28:36]) == "OpusHead" {
			return nil, i18n.New("importer.theOggOpusFormatIs")
		}
		return newVorbisDecoder(f)
	case hasID3(head), head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return newMP3Decoder(f)
	}
	return nil, ErrUnsupported
}

func hasID3(b []byte) bool { return len(b) >= 10 && string(b[0:3]) == "ID3" }

func afterID3(f *os.File, head []byte) string {
	size := int64(head[6]&0x7F)<<21 | int64(head[7]&0x7F)<<14 | int64(head[8]&0x7F)<<7 | int64(head[9]&0x7F)
	var b [4]byte
	if _, err := f.ReadAt(b[:], 10+size); err != nil {
		return ""
	}
	return string(b[:])
}

type mp3Decoder struct {
	d   *mp3.Decoder
	raw []byte
}

func newMP3Decoder(f *os.File) (decoder, error) {
	d, err := mp3.NewDecoder(f)
	if err != nil {
		return nil, i18n.New("importer.invalidMp3File", err)
	}
	return &mp3Decoder{d: d}, nil
}

func (m *mp3Decoder) Info() info {
	// The decoder always yields 16-bit stereo.
	return info{Rate: m.d.SampleRate(), Channels: 2, Bits: 16, Frames: m.d.Length() / 4}
}

func (m *mp3Decoder) Next(dst []float32) (int, error) {
	n := len(dst) / 2 * 2
	if cap(m.raw) < n*2 {
		m.raw = make([]byte, n*2)
	}
	b := m.raw[:n*2]
	got, err := io.ReadFull(m.d, b)
	got -= got % 4
	for i := 0; i < got/2; i++ {
		dst[i] = float32(int16(uint16(b[2*i])|uint16(b[2*i+1])<<8)) / 32768
	}
	if err == io.ErrUnexpectedEOF {
		err = nil
		if got == 0 {
			err = io.EOF
		}
	}
	if got == 0 && err == nil {
		err = io.EOF
	}
	return got / 2, err
}

type vorbisDecoder struct{ r *oggvorbis.Reader }

func newVorbisDecoder(f *os.File) (decoder, error) {
	r, err := oggvorbis.NewReader(bufio.NewReaderSize(f, 256<<10))
	if err != nil {
		return nil, i18n.New("importer.invalidOggFile", err)
	}
	return &vorbisDecoder{r: r}, nil
}

func (v *vorbisDecoder) Info() info {
	return info{Rate: v.r.SampleRate(), Channels: v.r.Channels(), Bits: 16, Frames: v.r.Length()}
}

func (v *vorbisDecoder) Next(dst []float32) (int, error) {
	n, err := v.r.Read(dst)
	if n > 0 && err == io.EOF {
		err = nil
	}
	return n, err
}

type flacDecoder struct {
	s    *flac.Stream
	bits int
	ch   int
	buf  []float32
}

func newFLACDecoder(f *os.File) (decoder, error) {
	s, err := flac.New(bufio.NewReaderSize(f, 256<<10))
	if err != nil {
		return nil, i18n.New("importer.invalidFlacFile", err)
	}
	return &flacDecoder{s: s, bits: int(s.Info.BitsPerSample), ch: int(s.Info.NChannels)}, nil
}

func (d *flacDecoder) Info() info {
	return info{Rate: int(d.s.Info.SampleRate), Channels: d.ch, Bits: d.bits, Frames: int64(d.s.Info.NSamples)}
}

func (d *flacDecoder) Next(dst []float32) (int, error) {
	if len(d.buf) == 0 {
		fr, err := d.s.ParseNext()
		if err != nil {
			return 0, err
		}
		scale := float32(int64(1) << (d.bits - 1))
		d.buf = make([]float32, 0, int(fr.BlockSize)*d.ch)
		for i := 0; i < int(fr.BlockSize); i++ {
			for c := 0; c < d.ch; c++ {
				d.buf = append(d.buf, float32(fr.Subframes[c].Samples[i])/scale)
			}
		}
	}
	n := copy(dst[:len(dst)/d.ch*d.ch], d.buf)
	d.buf = d.buf[n:]
	return n, nil
}
