package exporter

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"

	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/wav"
)

// sineSide writes a side file of the given length and returns it with markers.
func sineSide(t *testing.T, key string, f wav.Format, seconds int, markers []project.Marker) Side {
	t.Helper()
	path := filepath.Join(t.TempDir(), "side.wav")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(file, f)
	if err != nil {
		t.Fatal(err)
	}
	frames := seconds * f.SampleRate
	buf := make([]byte, 0, frames*f.BlockAlign())
	for i := 0; i < frames; i++ {
		v := int32(math.Sin(2*math.Pi*440*float64(i)/float64(f.SampleRate)) * 12000)
		for c := 0; c < f.Channels; c++ {
			if f.BitDepth == 16 {
				buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(v)))
			} else {
				buf = append(buf, 0, byte(v), byte(v>>8))
			}
		}
	}
	if _, err := w.Write(buf); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	return Side{Key: key, Path: path, Format: f, Frames: int64(frames), Markers: markers}
}

func mk(startMs, endMs int64, title string) []project.Marker {
	return []project.Marker{{Kind: project.MarkerStart, PositionMs: startMs, Title: title}, {Kind: project.MarkerEnd, PositionMs: endMs}}
}

func testCover(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{200, 10, 10, 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTracksNumberAcrossSidesAndNamesAreSafe(t *testing.T) {
	f := wav.Format{SampleRate: 8000, Channels: 1, BitDepth: 16}
	a := sineSide(t, "A", f, 4, append(mk(0, 1000, "A/B: Intro?"), mk(2000, 3000, "")...))
	b := sineSide(t, "B", f, 2, mk(0, 1000, "A_B_ Intro_"))
	in := Input{Name: "Tape", Artist: "Band", Sides: []Side{a, b}}
	tracks := in.Tracks()
	if len(tracks) != 3 || tracks[2].Num != 3 || tracks[2].Total != 3 || tracks[0].Artist != "Band" {
		t.Fatalf("tracks = %+v", tracks)
	}
	got := in.Files(FormatMP3)
	want := []string{"01 - A_B_ Intro_.mp3", "Track 02.mp3", "03 - A_B_ Intro_.mp3"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	if SafeName("CON") != "_CON" || SafeName(" a.. ") != "a" {
		t.Fatal("device names and trailing dots must be handled")
	}
	if names := uniqueNames([]string{"a.wav", "A.wav"}); names[1] != "A (2).wav" {
		t.Fatalf("names = %v", names)
	}
}

func TestCoverPNGConvertsJPEG(t *testing.T) {
	out, err := CoverPNG(testCover(t))
	if err != nil || !bytes.HasPrefix(out, pngSignature) {
		t.Fatalf("not a PNG: %v", err)
	}
	if again, _ := CoverPNG(out); !bytes.Equal(again, out) {
		t.Fatal("a PNG must pass through unchanged")
	}
}

func run(t *testing.T, in Input, o Options) []string {
	t.Helper()
	o.Dir = t.TempDir()
	names, err := Run(context.Background(), in, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {})
	for i := range names {
		names[i] = filepath.Join(o.Dir, names[i])
	}
	return names
}

func TestWAVAndMP3TracksCarryID3v24WithCover(t *testing.T) {
	f := wav.Format{SampleRate: 44100, Channels: 2, BitDepth: 16}
	in := Input{Name: "Tape", Artist: "Band", Year: "1985", Genre: "Rock", Cover: testCover(t),
		Sides: []Side{sineSide(t, "A", f, 3, mk(500, 2500, "Song"))}}

	files := run(t, in, Options{Format: FormatMP3, Bitrate: 192, EmbedCover: true, CoverMaxMB: 1})
	if len(files) != 2 || filepath.Base(files[1]) != CoverName {
		t.Fatalf("files = %v", files)
	}
	tag, err := id3v2.Open(files[0], id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tag.Close()
	if tag.Version() != 4 || tag.Title() != "Song" || tag.Artist() != "Band" || tag.GetTextFrame("TRCK").Text != "1/1" {
		t.Fatalf("tag: v%d %q %q", tag.Version(), tag.Title(), tag.Artist())
	}
	if pics := tag.GetFrames("APIC"); len(pics) != 1 || !bytes.HasPrefix(pics[0].(id3v2.PictureFrame).Picture, pngSignature) {
		t.Fatal("the cover must be embedded as PNG")
	}
	raw, _ := os.ReadFile(files[0])
	dec, err := mp3.NewDecoder(bytes.NewReader(raw[tag.Size():]))
	if err != nil {
		t.Fatal(err)
	}
	if sec := float64(dec.Length()) / 4 / 44100; math.Abs(sec-2) > 0.15 {
		t.Fatalf("mp3 lasts %.2f s, want 2", sec)
	}

	files = run(t, in, Options{Format: FormatWAV, EmbedCover: false})
	raw, _ = os.ReadFile(files[0])
	if len(raw) < 44 || string(raw[36:40]) != "data" {
		t.Fatal("bad WAV header")
	}
	data := int(binary.LittleEndian.Uint32(raw[40:]))
	if data != 2*44100*4 || int(binary.LittleEndian.Uint32(raw[4:])) != len(raw)-8 {
		t.Fatalf("data %d, riff size %d, file %d", data, binary.LittleEndian.Uint32(raw[4:]), len(raw))
	}
	if string(raw[44+data:48+data]) != "id3 " || string(raw[52+data:55+data]) != "ID3" || raw[55+data] != 4 {
		t.Fatal("the ID3v2.4 tag must follow the audio in an id3 chunk")
	}
}

func TestFLACAlbumImageMatchesCue(t *testing.T) {
	f := wav.Format{SampleRate: 44100, Channels: 2, BitDepth: 16}
	in := Input{Name: "Tape", Album: "Live", Artist: "Band", Sides: []Side{
		sineSide(t, "A", f, 4, append(mk(0, 1000, "One"), mk(2000, 3500, "Two")...)),
		sineSide(t, "B", f, 2, mk(0, 1000, "Three")),
	}}
	files := run(t, in, Options{Format: FormatFLACCue})
	if len(files) != 2 || filepath.Base(files[0]) != "Live.flac" || filepath.Base(files[1]) != "Live.cue" {
		t.Fatalf("files = %v", files)
	}
	st, err := flac.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n uint64
	for {
		fr, err := st.ParseNext()
		if err != nil {
			break
		}
		n += uint64(fr.Subframes[0].NSamples)
	}
	if want := uint64(3500 * 44100 / 1000); n != want {
		t.Fatalf("decoded %d frames, want %d", n, want)
	}
	cue, _ := os.ReadFile(files[1])
	for _, want := range []string{"INDEX 01 00:00:00", "INDEX 01 00:01:00", "INDEX 01 00:02:37", `TITLE "Three"`} {
		if !strings.Contains(string(cue), want) {
			t.Fatalf("cue misses %q:\n%s", want, cue)
		}
	}

	in.Sides[1].Format.SampleRate = 22050
	if p := in.Plan(); p.Formats[2].Available || !p.Formats[0].Available {
		t.Fatalf("sides of different formats must refuse the album image only: %+v", p.Formats)
	}
}

func TestMP3RefusesUnsupportedRateAndCancelRemovesFiles(t *testing.T) {
	hi := sineSide(t, "A", wav.Format{SampleRate: 96000, Channels: 2, BitDepth: 16}, 1, mk(0, 500, "x"))
	if p := (Input{Sides: []Side{hi}}).Plan(); p.Formats[1].Available || p.Formats[1].Reason == "" {
		t.Fatal("96 kHz must disable MP3 with a reason")
	}
	f := wav.Format{SampleRate: 44100, Channels: 1, BitDepth: 24}
	in := Input{Name: "T", Sides: []Side{sineSide(t, "A", f, 2, append(mk(0, 500, "a"), mk(600, 1500, "b")...))}}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Run(ctx, in, Options{Format: FormatMP3, Bitrate: 128, Dir: dir}, func(done, total int64) { cancel() })
	if err == nil {
		t.Fatal("a cancelled export must fail")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("cancel left %d files", len(left))
	}
}

func TestWAVResamplingKeepsLengthPitchAndLevel(t *testing.T) {
	for _, tc := range []struct{ from, to int }{{44100, 48000}, {96000, 44100}} {
		f := wav.Format{SampleRate: tc.from, Channels: 2, BitDepth: 16}
		in := Input{Name: "Tape", Sides: []Side{sineSide(t, "A", f, 3, mk(500, 2500, "Song"))}}
		files := run(t, in, Options{Format: FormatWAV, SampleRate: tc.to})
		raw, _ := os.ReadFile(files[0])
		if int(binary.LittleEndian.Uint32(raw[24:])) != tc.to {
			t.Fatalf("%d: header rate %d", tc.from, binary.LittleEndian.Uint32(raw[24:]))
		}
		data := int(binary.LittleEndian.Uint32(raw[40:]))
		if data != 2*tc.to*4 {
			t.Fatalf("%d: data %d, want %d", tc.from, data, 2*tc.to*4)
		}
		var sum float64
		crossings := 0
		prev := int16(0)
		n := data / 4
		for i := 0; i < n; i++ {
			v := int16(binary.LittleEndian.Uint16(raw[44+i*4:]))
			sum += float64(v) * float64(v)
			if prev < 0 && v >= 0 {
				crossings++
			}
			prev = v
		}
		hz := float64(crossings) / 2
		rms := math.Sqrt(sum / float64(n))
		if math.Abs(hz-440) > 2 || math.Abs(rms-8485) > 150 {
			t.Fatalf("%d->%d: %.1f Hz, rms %.0f", tc.from, tc.to, hz, rms)
		}
	}
	f := wav.Format{SampleRate: 44100, Channels: 1, BitDepth: 16}
	in := Input{Name: "Tape", Sides: []Side{sineSide(t, "A", f, 3, mk(500, 2500, "Song"))}}
	if _, err := Run(context.Background(), in, Options{Format: FormatWAV, SampleRate: 12345, Dir: t.TempDir()}, nil); err == nil {
		t.Fatal("an unsupported rate must be refused")
	}
}

func TestEmbeddedCoverIsShrunkButCoverFileIsNot(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 600, 400))
	rnd := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(rnd.Intn(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var big bytes.Buffer
	if err := png.Encode(&big, img); err != nil {
		t.Fatal(err)
	}
	limit := 100 << 10
	if big.Len() <= limit {
		t.Fatalf("test cover is only %d bytes", big.Len())
	}
	small, err := ShrinkPNG(big.Bytes(), limit)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(small))
	if len(small) > limit || math.Abs(float64(cfg.Width)/float64(cfg.Height)-1.5) > 0.03 {
		t.Fatalf("shrunk to %d bytes, %dx%d", len(small), cfg.Width, cfg.Height)
	}
	if same, _ := ShrinkPNG(small, limit); !bytes.Equal(same, small) {
		t.Fatal("a cover that fits must stay unchanged")
	}

	f := wav.Format{SampleRate: 8000, Channels: 1, BitDepth: 16}
	in := Input{Name: "Tape", Cover: big.Bytes(), Sides: []Side{sineSide(t, "A", f, 3, mk(0, 2000, "Song"))}}
	files := run(t, in, Options{Format: FormatWAV, EmbedCover: true, CoverMaxMB: float64(limit) / (1 << 20)})
	got, _ := os.ReadFile(files[len(files)-1])
	if filepath.Base(files[len(files)-1]) != CoverName || !bytes.Equal(got, big.Bytes()) {
		t.Fatal("cover.png must keep the original bytes")
	}
	raw, _ := os.ReadFile(files[0])
	if len(raw) > 44+2*8000*2+limit+4096 {
		t.Fatalf("wav is %d bytes: the embedded cover was not limited", len(raw))
	}
	if _, err := Run(context.Background(), in, Options{Format: FormatMP3, Bitrate: 128, EmbedCover: true, Dir: t.TempDir()}, nil); err == nil {
		t.Fatal("a missing size limit must be refused")
	}
}

func TestCompilationTracksHaveVariousArtistsAlbumArtist(t *testing.T) {
	f := wav.Format{SampleRate: 8000, Channels: 1, BitDepth: 16}
	markers := mk(0, 1000, "One")
	markers[0].AlbumArtist = "Someone"
	in := Input{Name: "Mix", Artist: "Band", Sides: []Side{sineSide(t, "A", f, 2, markers)}}
	if got := in.Tracks()[0].AlbumArtist; got != "Someone" {
		t.Fatalf("album artist = %q, want the track value", got)
	}
	in.Compilation = true
	in.TrackArtist = "Band"
	if tr := in.Tracks()[0]; tr.AlbumArtist != "V/A" || tr.Artist != "Band" {
		t.Fatalf("album artist = %q, artist = %q, want V/A and Band", tr.AlbumArtist, tr.Artist)
	}
}

func TestExportOriginalCopiesStoredSides(t *testing.T) {
	f := wav.Format{SampleRate: 44100, Channels: 2, BitDepth: 16}
	a := sineSide(t, "A", f, 3, mk(500, 2500, "Song"))
	in := Input{Name: "Tape", Sides: []Side{a}, Originals: []Original{
		{Name: OriginalName("A"), Path: a.Path},
		{Name: OriginalName("B"), Path: filepath.Join(t.TempDir(), "missing.wav")},
	}}
	if got := in.FilesFor(Options{Format: FormatWAV}); len(got) != 1 {
		t.Fatalf("off must add nothing: %v", got)
	}
	want, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in.Originals = in.Originals[:1]
	names, err := Run(context.Background(), in, Options{Format: FormatFLACCue, Dir: dir, ExportOriginal: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "side-a-recording.wav"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("original differs: %v", err)
	}
	if len(names) != len(in.FilesFor(Options{Format: FormatFLACCue, ExportOriginal: true})) {
		t.Fatalf("files: %v", names)
	}
	dir = t.TempDir()
	if _, err = Run(context.Background(), in, Options{Format: FormatWAV, Dir: dir}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "side-a-recording.wav")); err == nil {
		t.Fatal("off must not copy the original")
	}
}
