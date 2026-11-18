package exporter

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/braheezy/shine-mp3/pkg/mp3"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"

	"github.com/afrunt/tape2digi/internal/i18n"
)

// MP3Bitrates are the CBR bitrates (kbps) the dialog offers.
var MP3Bitrates = []int{128, 192, 256, 320}

const wavHeaderSize = 44

// Options are the user's export choices.
type Options struct {
	Format     string  `json:"format"`
	Bitrate    int     `json:"bitrate"`
	SampleRate int     `json:"sampleRate"` // WAV only; 0 keeps the recording rate
	EmbedCover bool    `json:"embedCover"`
	CoverMaxMB float64 `json:"coverMaxMB"` // limit for the cover in MP3 and WAV tags
	Dir        string  `json:"dir"`
	// ExportOriginal also copies the stored side recordings as WAV files.
	ExportOriginal bool `json:"exportOriginal"`
}

// FormatInfo tells whether a format can be exported and why not.
type FormatInfo struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Bitrates  []int  `json:"bitrates,omitempty"`
	Rates     []int  `json:"rates,omitempty"` // WAV target sample rates
	Rate      int    `json:"rate,omitempty"`  // recording rate when all tracks share it
}

// Plan summarises what can be exported.
type Plan struct {
	Tracks     int          `json:"tracks"`
	DurationMs int64        `json:"durationMs"`
	HasCover   bool         `json:"hasCover"`
	Formats    []FormatInfo `json:"formats"`
	// Originals are the file names the original-recording option would write.
	Originals []string `json:"originals"`
}

// mp3Bitrates lists the offered bitrates the encoder supports at a sample rate.
func mp3Bitrates(rate int) []int {
	var out []int
	for _, b := range MP3Bitrates {
		if _, err := mp3.CheckConfig(rate, b); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// Plan reports the track count and which formats are possible.
func (in Input) Plan() Plan {
	tracks := in.Tracks()
	p := Plan{Tracks: len(tracks), HasCover: len(in.Cover) > 0}
	for _, t := range tracks {
		p.DurationMs += t.DurationMs()
	}
	for _, o := range in.Originals {
		p.Originals = append(p.Originals, o.Name)
	}
	wavInfo := FormatInfo{ID: FormatWAV, Available: len(tracks) > 0, Rates: WAVRates}
	if len(tracks) > 0 {
		wavInfo.Rate = tracks[0].Side.Format.SampleRate
		for _, t := range tracks {
			if t.Side.Format.SampleRate != wavInfo.Rate {
				wavInfo.Rate = 0
			}
		}
	}
	mp3Info := FormatInfo{ID: FormatMP3, Available: len(tracks) > 0}
	flacInfo := FormatInfo{ID: FormatFLACCue, Available: len(tracks) > 0}
	if len(tracks) > 0 {
		for _, t := range tracks {
			if len(mp3Bitrates(t.Side.Format.SampleRate)) == 0 {
				mp3Info.Available = false
				mp3Info.Reason = i18n.T("export.mp3Rate", t.Side.Format.SampleRate)
				break
			}
		}
		if mp3Info.Available {
			mp3Info.Bitrates = mp3Bitrates(tracks[0].Side.Format.SampleRate)
			for _, t := range tracks[1:] {
				mp3Info.Bitrates = intersect(mp3Info.Bitrates, mp3Bitrates(t.Side.Format.SampleRate))
			}
		}
		if err := sameSides(tracks); err != nil {
			flacInfo.Available = false
			flacInfo.Reason = err.Error()
		}
	}
	p.Formats = []FormatInfo{wavInfo, mp3Info, flacInfo}
	return p
}

func intersect(a, b []int) []int {
	var out []int
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
			}
		}
	}
	return out
}

func sameSides(tracks []Track) error {
	f := tracks[0].Side.Format
	for _, t := range tracks[1:] {
		if t.Side.Format != f {
			return i18n.New("export.flacMismatch", f.SampleRate, f.Channels, f.BitDepth)
		}
	}
	return nil
}

// progress reports bytes of audio written.
type progress struct {
	done, total int64
	fn          func(done, total int64)
}

func (p *progress) add(n int64) {
	p.done += n
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
}

// ctxReader stops reading when the job is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// openPCM returns a reader over the track's PCM bytes.
func openPCM(ctx context.Context, t Track) (io.Reader, func(), error) {
	f, err := os.Open(t.Side.Path)
	if err != nil {
		return nil, nil, err
	}
	ba := int64(t.Side.Format.BlockAlign())
	sr := io.NewSectionReader(f, wavHeaderSize+t.StartFrame*ba, t.Frames*ba)
	return ctxReader{ctx, sr}, func() { _ = f.Close() }, nil
}

// Run writes the export into opts.Dir, replacing files of the same name. The
// files it created are removed when it fails or is cancelled.
func Run(ctx context.Context, in Input, opts Options, report func(done, total int64)) (written []string, err error) {
	tracks := in.Tracks()
	if len(tracks) == 0 {
		return nil, i18n.New("export.noTracks")
	}
	plan := in.Plan()
	var info FormatInfo
	for _, f := range plan.Formats {
		if f.ID == opts.Format {
			info = f
		}
	}
	if info.ID == "" {
		return nil, i18n.New("export.unknownFormat")
	}
	if !info.Available {
		return nil, errors.New(info.Reason)
	}
	if opts.Format == FormatMP3 {
		ok := false
		for _, b := range info.Bitrates {
			ok = ok || b == opts.Bitrate
		}
		if !ok {
			return nil, i18n.New("export.unsupportedBitrate", opts.Bitrate)
		}
	}
	if opts.Format == FormatWAV && opts.SampleRate != 0 {
		ok := false
		for _, r := range WAVRates {
			ok = ok || r == opts.SampleRate
		}
		if !ok {
			return nil, i18n.New("export.unsupportedRate", opts.SampleRate)
		}
	}
	if st, e := os.Stat(opts.Dir); e != nil || !st.IsDir() {
		return nil, i18n.New("export.badFolder")
	}
	var cover, tagCover, coverFile []byte
	if len(in.Cover) > 0 {
		if coverFile, err = CoverPNG(in.Cover); err != nil {
			return nil, i18n.New("export.badCover", err)
		}
		if opts.EmbedCover {
			cover = coverFile
			if opts.Format == FormatWAV || opts.Format == FormatMP3 {
				if !(opts.CoverMaxMB > 0 && opts.CoverMaxMB <= MaxCoverMB) {
					return nil, i18n.New("export.badCoverLimit", MaxCoverMB)
				}
				if tagCover, err = ShrinkPNG(cover, int(opts.CoverMaxMB*1048576)); err != nil {
					return nil, err
				}
			}
		}
	}
	pr := &progress{fn: report}
	for _, t := range tracks {
		pr.total += t.Frames * int64(t.Side.Format.BlockAlign())
	}
	names := in.Files(opts.Format)
	if opts.ExportOriginal {
		for _, o := range in.Originals {
			if st, e := os.Stat(o.Path); e == nil {
				pr.total += st.Size()
			}
		}
	}
	defer func() {
		if err != nil {
			for _, n := range written {
				_ = os.Remove(filepath.Join(opts.Dir, n))
			}
			written = nil
		}
	}()
	// write creates the file through a temporary name so a failure never
	// leaves half a file behind.
	write := func(name string, fill func(w *os.File) error) error {
		final := filepath.Join(opts.Dir, name)
		tmp := final + ".part"
		f, e := os.Create(tmp)
		if e != nil {
			return e
		}
		if e = fill(f); e == nil {
			e = f.Close()
		} else {
			_ = f.Close()
		}
		if e == nil {
			e = os.Rename(tmp, final)
		}
		if e != nil {
			_ = os.Remove(tmp)
			return e
		}
		written = append(written, name)
		return nil
	}

	switch opts.Format {
	case FormatWAV, FormatMP3:
		for i, t := range tracks {
			tag, e := id3Tag(in, t, tagCover)
			if e != nil {
				return written, e
			}
			e = write(names[i], func(w *os.File) error {
				if opts.Format == FormatWAV {
					return writeWAV(ctx, w, t, tag, opts.SampleRate, pr)
				}
				return writeMP3(ctx, w, t, tag, opts.Bitrate, pr)
			})
			if e != nil {
				return written, e
			}
		}
	case FormatFLACCue:
		if err = write(names[0], func(w *os.File) error { return writeFLAC(ctx, w, in, tracks, cover, names[0], pr) }); err != nil {
			return written, err
		}
		if err = write(names[1], func(w *os.File) error {
			_, e := w.WriteString(cueSheet(in, tracks, names[0]))
			return e
		}); err != nil {
			return written, err
		}
	}
	if opts.ExportOriginal {
		for _, o := range in.Originals {
			if err = write(o.Name, func(w *os.File) error { return copyFile(ctx, w, o.Path, pr) }); err != nil {
				return written, err
			}
		}
	}
	if len(coverFile) > 0 {
		if err = write(CoverName, func(w *os.File) error { _, e := w.Write(coverFile); return e }); err != nil {
			return written, err
		}
	}
	return written, ctx.Err()
}

// copyFile streams the file at path into w, reporting progress.
func copyFile(ctx context.Context, w io.Writer, path string, pr *progress) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	r := ctxReader{ctx, f}
	for {
		n, e := r.Read(buf)
		if n > 0 {
			if _, we := w.Write(buf[:n]); we != nil {
				return we
			}
			pr.add(int64(n))
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
	}
}

func put16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func put32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// writeWAV writes the track as PCM in the recording's format with the ID3v2.4
// tag in an "id3 " chunk after the audio.
func writeWAV(ctx context.Context, w io.Writer, t Track, tag []byte, rate int, pr *progress) error {
	f := t.Side.Format
	frames := t.Frames
	var rs *resampler
	var r io.Reader
	if rate != 0 && rate != f.SampleRate {
		pcm, err := readTrack(ctx, t)
		if err != nil {
			return err
		}
		rs = newResampler(pcm, f.SampleRate, rate, f.Channels, f.BitDepth)
		frames = rs.outFrames()
	} else {
		rate = f.SampleRate
		var closeFn func()
		var err error
		if r, closeFn, err = openPCM(ctx, t); err != nil {
			return err
		}
		defer closeFn()
	}
	data := frames * int64(f.BlockAlign())
	pad := len(tag) % 2
	if data+int64(len(tag)+pad)+44 > 0xFFFFFFFF {
		return i18n.New("export.trackTooLarge")
	}
	h := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(h[4:], uint32(36+data+8+int64(len(tag)+pad)))
	h = append(h, "WAVEfmt "...)
	h = put32(h, 16)
	h = put16(h, 1)
	h = put16(h, uint16(f.Channels))
	h = put32(h, uint32(rate))
	h = put32(h, uint32(rate*f.BlockAlign()))
	h = put16(h, uint16(f.BlockAlign()))
	h = put16(h, uint16(f.BitDepth))
	h = append(h, "data"...)
	h = put32(h, uint32(data))
	if _, err := w.Write(h); err != nil {
		return err
	}
	if rs != nil {
		// Progress counts source bytes so the bar matches the other formats.
		srcBytes := t.Frames * int64(f.BlockAlign())
		var written, reported int64
		err := rs.run(ctx, func(b []byte) error {
			if _, e := w.Write(b); e != nil {
				return e
			}
			written += int64(len(b))
			if data > 0 {
				target := srcBytes * written / data
				pr.add(target - reported)
				reported = target
			}
			return nil
		})
		if err != nil {
			return err
		}
		pr.add(srcBytes - reported)
	} else {
		buf := make([]byte, 256<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
				pr.add(int64(n))
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
	}
	tail := append([]byte("id3 "), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(tail[4:], uint32(len(tag)))
	tail = append(tail, tag...)
	if pad == 1 {
		tail = append(tail, 0)
	}
	_, err := w.Write(tail)
	return err
}

// readTrack loads the track's PCM into memory.
func readTrack(ctx context.Context, t Track) ([]byte, error) {
	r, closeFn, err := openPCM(ctx, t)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return io.ReadAll(r)
}

// mp3Setup returns an encoder set to the CBR bitrate. The encoder derives its
// frame layout from a fixed 128 kbps in the constructor, so the bitrate fields
// are recomputed here.
func mp3Setup(rate, channels, kbps int) (*mp3.Encoder, error) {
	if _, err := mp3.CheckConfig(rate, kbps); err != nil {
		return nil, err
	}
	enc := mp3.NewEncoder(rate, channels)
	table := []int{-1, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	if rate < 32000 {
		table = []int{-1, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	}
	idx := -1
	for i, b := range table {
		if b == kbps {
			idx = i
		}
	}
	if idx < 0 {
		return nil, errors.New("unsupported bitrate")
	}
	avg := float64(enc.Mpeg.GranulesPerFrame) * 576 / float64(rate) * float64(kbps) * 1000 / float64(enc.Mpeg.BitsPerSlot)
	enc.Mpeg.Bitrate = int64(kbps)
	enc.Mpeg.BitrateIndex = int64(idx)
	enc.Mpeg.WholeSlotsPerFrame = int64(avg)
	enc.Mpeg.FracSlotsPerFrame = avg - float64(int64(avg))
	enc.Mpeg.SlotLag = -enc.Mpeg.FracSlotsPerFrame
	enc.Mpeg.Padding = 0
	return enc, nil
}

// writeMP3 writes the ID3v2.4 tag and the CBR stream. 24-bit audio is reduced
// to its upper 16 bits.
func writeMP3(ctx context.Context, w io.Writer, t Track, tag []byte, kbps int, pr *progress) error {
	f := t.Side.Format
	enc, err := mp3Setup(f.SampleRate, f.Channels, kbps)
	if err != nil {
		return err
	}
	if _, err = w.Write(tag); err != nil {
		return err
	}
	r, closeFn, err := openPCM(ctx, t)
	if err != nil {
		return err
	}
	defer closeFn()
	const frames = 1152
	bps := f.BitDepth / 8
	raw := make([]byte, frames*f.BlockAlign())
	samples := make([]int16, frames*f.Channels)
	emit := func() error {
		out, n := enc.EncodeBufferInterleaved(samples)
		_, werr := w.Write(out[:n])
		return werr
	}
	for {
		n, err := io.ReadFull(r, raw)
		if rerr := readerErr(err); rerr != nil {
			return rerr
		}
		if n == 0 {
			break
		}
		pr.add(int64(n))
		for i := range samples {
			if (i+1)*bps <= n {
				samples[i] = int16(binary.LittleEndian.Uint16(raw[i*bps+bps-2:]))
			} else {
				samples[i] = 0
			}
		}
		if werr := emit(); werr != nil {
			return werr
		}
		if err != nil {
			break
		}
	}
	clear(samples)
	return emit() // flushes the encoder delay
}

func readerErr(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

// writeFLAC writes all tracks back to back as one FLAC stream.
func writeFLAC(ctx context.Context, w *os.File, in Input, tracks []Track, cover []byte, flacName string, pr *progress) error {
	f := tracks[0].Side.Format
	var total uint64
	for _, t := range tracks {
		total += uint64(t.Frames)
	}
	const block = 4096
	info := &meta.StreamInfo{
		BlockSizeMin: block, BlockSizeMax: block,
		SampleRate: uint32(f.SampleRate), NChannels: uint8(f.Channels), BitsPerSample: uint8(f.BitDepth),
		NSamples: total,
	}
	tags := [][2]string{{"ALBUM", in.AlbumName()}, {"CUESHEET", cueSheet(in, tracks, flacName)}}
	add := func(k, v string) {
		if v != "" {
			tags = append(tags, [2]string{k, v})
		}
	}
	add("ARTIST", in.Artist)
	add("DATE", in.Year)
	add("GENRE", in.Genre)
	blocks := []*meta.Block{{Header: meta.Header{Type: meta.TypeVorbisComment, Length: 1}, Body: &meta.VorbisComment{Vendor: "tape2digi", Tags: tags}}}
	if len(cover) > 0 {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(cover))
		if err != nil {
			return err
		}
		blocks = append(blocks, &meta.Block{Header: meta.Header{Type: meta.TypePicture, Length: 1}, Body: &meta.Picture{
			Type: 3, MIME: "image/png", Desc: "Front cover",
			Width: uint32(cfg.Width), Height: uint32(cfg.Height), Depth: 24, Data: cover,
		}})
	}
	// The encoder must not close the file: it only needs to seek to patch the stream info.
	enc, err := flac.NewEncoder(struct{ io.WriteSeeker }{w}, info, blocks...)
	if err != nil {
		return err
	}
	ch := frame.ChannelsMono
	if f.Channels == 2 {
		ch = frame.ChannelsLR
	}
	bps := f.BitDepth / 8
	ba := f.BlockAlign()
	raw := make([]byte, block*ba)
	pending := 0 // frames carried over between tracks so every block but the last is full
	cols := make([][]int32, f.Channels)
	for i := range cols {
		cols[i] = make([]int32, 0, block)
	}
	flush := func() error {
		if pending == 0 {
			return nil
		}
		fr := &frame.Frame{Header: frame.Header{HasFixedBlockSize: true, BlockSize: uint16(pending), SampleRate: uint32(f.SampleRate), Channels: ch, BitsPerSample: uint8(f.BitDepth)}}
		for c := range cols {
			fr.Subframes = append(fr.Subframes, &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: cols[c], NSamples: pending})
			cols[c] = make([]int32, 0, block)
		}
		pending = 0
		return enc.WriteFrame(fr)
	}
	for _, t := range tracks {
		r, closeFn, err := openPCM(ctx, t)
		if err != nil {
			return err
		}
		for {
			n, err := io.ReadFull(r, raw)
			frames := n / ba
			pr.add(int64(n))
			for i := 0; i < frames; i++ {
				for c := 0; c < f.Channels; c++ {
					o := i*ba + c*bps
					var v int32
					if bps == 2 {
						v = int32(int16(binary.LittleEndian.Uint16(raw[o:])))
					} else {
						v = int32(raw[o])<<8 | int32(raw[o+1])<<16 | int32(int8(raw[o+2]))<<24
						v >>= 8
					}
					cols[c] = append(cols[c], v)
				}
				if pending++; pending == block {
					if ferr := flush(); ferr != nil {
						closeFn()
						return ferr
					}
				}
			}
			if rerr := readerErr(err); rerr != nil {
				closeFn()
				return rerr
			}
			if err != nil {
				break
			}
		}
		closeFn()
	}
	if err := flush(); err != nil {
		return err
	}
	return enc.Close()
}
