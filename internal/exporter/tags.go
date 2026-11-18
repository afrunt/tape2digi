package exporter

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // cover decoder
	"image/png"
	"math"
	"strings"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/bogem/id3v2/v2"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// CoverPNG returns the cover as PNG; a PNG is passed through unchanged and any
// other supported image is re-encoded.
func CoverPNG(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if bytes.HasPrefix(data, pngSignature) {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// id3Tag builds an ID3v2.4.0 tag for a track; cover is PNG or nil.
func id3Tag(in Input, t Track, cover []byte) ([]byte, error) {
	tag := id3v2.NewEmptyTag()
	tag.SetVersion(4)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	text := func(id, v string) {
		if v != "" {
			tag.AddTextFrame(id, id3v2.EncodingUTF8, v)
		}
	}
	text("TIT2", t.TitleOrDefault())
	text("TPE1", t.Artist)
	text("TPE2", t.AlbumArtist)
	text("TALB", in.AlbumName())
	text("TDRC", in.Year)
	text("TCON", in.Genre)
	text("TRCK", fmt.Sprintf("%d/%d", t.Num, t.Total))
	if in.Compilation {
		text("TCMP", "1")
	}
	if len(cover) > 0 {
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding:    id3v2.EncodingUTF8,
			MimeType:    "image/png",
			PictureType: id3v2.PTFrontCover,
			Description: "Front cover",
			Picture:     cover,
		})
	}
	var buf bytes.Buffer
	if _, err := tag.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func cueText(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", `"`, "'").Replace(s)
	return `"` + s + `"`
}

// cueTime formats a frame offset as mm:ss:ff with 75 frames per second.
func cueTime(offset, rate int64) string {
	f := offset * 75 / rate
	return fmt.Sprintf("%02d:%02d:%02d", f/(75*60), f/75%60, f%75)
}

// cueSheet describes the tracks of the album image; each track starts where
// the previous one ends.
func cueSheet(in Input, tracks []Track, flacName string) string {
	var b strings.Builder
	performer := in.Artist
	if performer == "" {
		performer = "Various Artists"
	}
	if in.Genre != "" {
		fmt.Fprintf(&b, "REM GENRE %s\n", cueText(in.Genre))
	}
	if in.Year != "" {
		fmt.Fprintf(&b, "REM DATE %s\n", in.Year)
	}
	fmt.Fprintf(&b, "PERFORMER %s\nTITLE %s\nFILE %s WAVE\n", cueText(performer), cueText(in.AlbumName()), cueText(flacName))
	var at int64
	rate := int64(tracks[0].Side.Format.SampleRate)
	for _, t := range tracks {
		fmt.Fprintf(&b, "  TRACK %02d AUDIO\n    TITLE %s\n", t.Num, cueText(t.TitleOrDefault()))
		if t.Artist != "" {
			fmt.Fprintf(&b, "    PERFORMER %s\n", cueText(t.Artist))
		}
		fmt.Fprintf(&b, "    INDEX 01 %s\n", cueTime(at, rate))
		at += t.Frames
	}
	return b.String()
}

// MaxCoverMB is the largest limit the export accepts for an embedded cover.
const MaxCoverMB = 50

const minCoverSide = 16

// ShrinkPNG returns the PNG unchanged when it is at most maxBytes, and
// otherwise a proportionally scaled-down PNG that fits.
func ShrinkPNG(data []byte, maxBytes int) ([]byte, error) {
	if len(data) <= maxBytes {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	src := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	// PNG size grows roughly with the pixel count, so start from the area ratio.
	f := math.Sqrt(float64(maxBytes)/float64(len(data))) * 0.95
	for {
		w, h := int(float64(b.Dx())*f), int(float64(b.Dy())*f)
		if w < minCoverSide || h < minCoverSide {
			return nil, i18n.New("export.coverLimit")
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, boxScale(src, w, h)); err != nil {
			return nil, err
		}
		if buf.Len() <= maxBytes {
			return buf.Bytes(), nil
		}
		f *= 0.85
	}
}

// boxScale shrinks by averaging the source pixels that fall in each target pixel.
func boxScale(src *image.RGBA, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n int
			for yy := y0; yy < y1; yy++ {
				row := src.Pix[yy*src.Stride:]
				for xx := x0; xx < x1; xx++ {
					p := row[xx*4:]
					r, g, bl, a, n = r+int(p[0]), g+int(p[1]), bl+int(p[2]), a+int(p[3]), n+1
				}
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}
