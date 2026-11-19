// Package export writes the marked tracks of a project as WAV or MP3 files,
// or as one FLAC album image with a CUE sheet, together with the cover.
package exporter

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/wav"
)

// Export formats.
const (
	FormatWAV     = "wav"
	FormatMP3     = "mp3"
	FormatFLACCue = "flac-cue"
)

// CoverName is the file the cover is exported as.
const CoverName = "cover.png"

// Side is one cassette side that is read for export: its audio file in the
// canonical PCM layout and its effective markers.
type Side struct {
	Key     string
	Path    string
	Format  wav.Format
	Frames  int64
	Markers []project.Marker
}

// Input is everything the export needs from the project.
type Input struct {
	Name        string
	Sides       []Side // A then B
	Artist      string // empty for a compilation
	TrackArtist string // default artist of tracks; falls back to Artist
	Compilation bool
	Year        string
	Album       string
	Genre       string
	// Cover holds the cover as stored (PNG or JPEG); empty when there is none.
	Cover []byte
	// Originals are the stored recordings of the sides that exist, offered
	// by the "Export Original Recording" option.
	Originals []Original
}

// Original is a stored side recording that is copied unchanged.
type Original struct {
	Name string // file name in the export folder
	Path string
}

// OriginalName is the exported name of the original recording of a side.
func OriginalName(key string) string { return "side-" + strings.ToLower(key) + "-recording.wav" }

// Track is one marked track in album order.
type Track struct {
	Num         int
	Total       int
	Side        *Side
	StartFrame  int64
	Frames      int64
	Title       string
	Artist      string
	AlbumArtist string
}

// DurationMs is the track length.
func (t Track) DurationMs() int64 { return t.Frames * 1000 / int64(t.Side.Format.SampleRate) }

// AlbumName is the album tag: the album, falling back to the project name.
func (in Input) AlbumName() string {
	if in.Album != "" {
		return in.Album
	}
	return in.Name
}

// Tracks lists the complete tracks (a start marker followed by an end marker)
// of side A, then side B. An empty artist follows the project artist.
func (in Input) Tracks() []Track {
	var out []Track
	for i := range in.Sides {
		s := &in.Sides[i]
		list := append([]project.Marker(nil), s.Markers...)
		sort.Slice(list, func(a, b int) bool { return list[a].PositionMs < list[b].PositionMs })
		rate := int64(s.Format.SampleRate)
		for j := 0; j+1 < len(list); j++ {
			st, en := list[j], list[j+1]
			if st.Kind != project.MarkerStart || en.Kind != project.MarkerEnd {
				continue
			}
			from, to := st.PositionMs*rate/1000, en.PositionMs*rate/1000
			if to > s.Frames {
				to = s.Frames
			}
			if to <= from {
				continue
			}
			t := Track{Side: s, StartFrame: from, Frames: to - from, Title: st.Title, Artist: st.Artist, AlbumArtist: st.AlbumArtist}
			if t.Artist == "" {
				t.Artist = in.TrackArtist
				if t.Artist == "" {
					t.Artist = in.Artist
				}
			}
			switch {
			case in.Compilation:
				t.AlbumArtist = VariousArtists
			case t.AlbumArtist == "":
				t.AlbumArtist = in.Artist
			}
			out = append(out, t)
		}
	}
	for i := range out {
		out[i].Num, out[i].Total = i+1, len(out)
	}
	return out
}

// VariousArtists is the album artist of every track of a compilation.
const VariousArtists = "V/A"

// TitleOrDefault is the track title, or "Track NN" when it is empty.
func (t Track) TitleOrDefault() string {
	if t.Title != "" {
		return t.Title
	}
	return fmt.Sprintf("Track %02d", t.Num)
}

const maxNameRunes = 120

// SafeName turns text into a file name part that is valid on Windows, macOS
// and Linux: forbidden characters become "_", the result is trimmed and kept
// reasonably short, and Windows device names are avoided.
func SafeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	out := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if utf8.RuneCountInString(out) > maxNameRunes {
		out = strings.TrimRight(string([]rune(out)[:maxNameRunes]), ". ")
	}
	base, _, _ := strings.Cut(out, ".")
	up := strings.ToUpper(strings.TrimSpace(base))
	switch {
	case up == "CON", up == "PRN", up == "AUX", up == "NUL",
		len(up) == 4 && (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) && up[3] >= '1' && up[3] <= '9':
		out = "_" + out
	}
	return out
}

// uniqueNames makes names unique ignoring case, as a case-insensitive file
// system would see them.
func uniqueNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, len(names))
	for i, n := range names {
		stem, ext := n, ""
		if dot := strings.LastIndex(n, "."); dot > 0 {
			stem, ext = n[:dot], n[dot:]
		}
		name := n
		for k := 2; seen[strings.ToLower(name)]; k++ {
			name = fmt.Sprintf("%s (%d)%s", stem, k, ext)
		}
		seen[strings.ToLower(name)] = true
		out[i] = name
	}
	return out
}

// DefaultNameTemplate names track files "NN - Title".
const DefaultNameTemplate = "{n} - {title}"

// MaxTemplateLen limits a file name template.
const MaxTemplateLen = 100

var templateToken = regexp.MustCompile(`\{[^{}]*\}`)

var templateTokens = map[string]bool{"{n}": true, "{title}": true, "{artist}": true, "{album}": true, "{year}": true, "{side}": true}

// CheckTemplate validates a file name template: known tokens only, no folder
// separators or characters that are not allowed in file names.
func CheckTemplate(tmpl string) error {
	tmpl = strings.TrimSpace(tmpl)
	switch {
	case tmpl == "":
		return i18n.New("export.templateEmpty")
	case utf8.RuneCountInString(tmpl) > MaxTemplateLen:
		return i18n.New("export.templateTooLong", MaxTemplateLen)
	}
	for _, r := range templateToken.ReplaceAllString(tmpl, "") {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*{}`, r) {
			return i18n.New("export.templateChars")
		}
	}
	for _, tok := range templateToken.FindAllString(tmpl, -1) {
		if !templateTokens[tok] {
			return i18n.New("export.templateToken", tok)
		}
	}
	return nil
}

// TrackFileName names a track file from the template (the default when it
// is empty or invalid). Every token value is made safe for file names; a track
// without a title is "Track NN" whatever the template.
func TrackFileName(in Input, t Track, tmpl, ext string) string {
	if t.Title == "" || SafeName(t.Title) == "" {
		return fmt.Sprintf("Track %02d%s", t.Num, ext)
	}
	if CheckTemplate(tmpl) != nil {
		tmpl = DefaultNameTemplate
	}
	side := ""
	if t.Side != nil {
		side = t.Side.Key
	}
	vals := map[string]string{
		"{n}": fmt.Sprintf("%02d", t.Num), "{title}": t.Title, "{artist}": t.Artist,
		"{album}": in.AlbumName(), "{year}": in.Year, "{side}": side,
	}
	if name := SafeName(expandTemplate(strings.TrimSpace(tmpl), vals)); name != "" {
		return name + ext
	}
	return fmt.Sprintf("Track %02d%s", t.Num, ext)
}

// expandTemplate replaces the tokens; an empty value also drops the literal
// text that only separated it, so "{artist} - {title}" without an artist is
// just the title.
func expandTemplate(tmpl string, vals map[string]string) string {
	type part struct {
		text  string
		token bool
	}
	var parts []part
	last := 0
	for _, loc := range templateToken.FindAllStringIndex(tmpl, -1) {
		if loc[0] > last {
			parts = append(parts, part{tmpl[last:loc[0]], false})
		}
		if v := SafeName(vals[tmpl[loc[0]:loc[1]]]); v != "" {
			parts = append(parts, part{v, true})
		} else {
			parts = append(parts, part{"", true})
		}
		last = loc[1]
	}
	if last < len(tmpl) {
		parts = append(parts, part{tmpl[last:], false})
	}
	var kept []part
	for _, p := range parts {
		if p.token && p.text == "" {
			continue
		}
		// Two literals meet where an empty token was: keep one separator.
		if !p.token && len(kept) > 0 && !kept[len(kept)-1].token {
			continue
		}
		kept = append(kept, p)
	}
	sep := func(p part) bool { return !p.token && strings.Trim(p.text, " -_.,;") == "" }
	for len(kept) > 0 && sep(kept[0]) {
		kept = kept[1:]
	}
	for len(kept) > 0 && sep(kept[len(kept)-1]) {
		kept = kept[:len(kept)-1]
	}
	var b strings.Builder
	for _, p := range kept {
		b.WriteString(p.text)
	}
	return b.String()
}

func (in Input) albumBase() string {
	if b := SafeName(in.AlbumName()); b != "" {
		return b
	}
	return "album"
}

// Files lists the names the export would write into the target folder,
// including cover.png when there is a cover.
func (in Input) Files(format string) []string { return in.FilesFor(Options{Format: format}) }

// FilesFor is Files for the given options, including the original
// recordings when they are asked for.
func (in Input) FilesFor(opts Options) []string {
	format := opts.Format
	var names []string
	switch format {
	case FormatWAV, FormatMP3:
		ext := ".wav"
		if format == FormatMP3 {
			ext = ".mp3"
		}
		for _, t := range in.Tracks() {
			names = append(names, TrackFileName(in, t, opts.NameTemplate, ext))
		}
	case FormatFLACCue:
		names = []string{in.albumBase() + ".flac", in.albumBase() + ".cue"}
	}
	names = uniqueNames(names)
	if len(in.Cover) > 0 {
		names = append(names, CoverName)
	}
	if opts.ExportOriginal {
		for _, o := range in.Originals {
			names = append(names, o.Name)
		}
	}
	return names
}
