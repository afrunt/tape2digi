// Package project implements the on-disk project: a directory with a
// tape2digi.json descriptor, the two cassette sides (side-A.wav, side-B.wav)
// and an optional cover image.
//
// Audio sides are committed to the descriptor as soon as they exist. Name and
// cover edits are staged in memory until Save is called.
package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/afrunt/tape2digi/internal/i18n"
	"image"
	_ "image/jpeg" // register decoders for cover validation
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/afrunt/tape2digi/internal/appdata"
	"github.com/afrunt/tape2digi/internal/wav"
)

const (
	// DescriptorName is the project descriptor file name.
	DescriptorName = "tape2digi.json"
	// SchemaVersion is the descriptor version this build writes. Version 1 is still read.
	SchemaVersion = 2
	// MaxCoverBytes is the largest accepted cover image.
	MaxCoverBytes = 20 << 20
	maxNameLen    = 200
)

// Side origins.
const (
	OriginRecorded = "recorded"
	OriginImported = "imported"
)

// SideKeys are the two cassette sides.
var SideKeys = []string{"A", "B"}

// ValidSide reports whether key is a side name.
func ValidSide(key string) bool { return key == "A" || key == "B" }

// SideFileName is the file name of a side inside the project directory.
func SideFileName(key string) string { return "side-" + key + ".wav" }

// Side describes one recorded or imported cassette side. File is relative to
// the project directory.
type Side struct {
	File       string    `json:"file"`
	Origin     string    `json:"origin"`
	RecordedAt time.Time `json:"recordedAt"`
	DurationMs int64     `json:"durationMs"`
	SampleRate int       `json:"sampleRate"`
	Channels   int       `json:"channels"`
	BitDepth   int       `json:"bitDepth"`
	Device     string    `json:"device,omitempty"`
	SourceName string    `json:"sourceName,omitempty"`
	Missing    bool      `json:"missing,omitempty"`
	// Staged marks a recording or import that is kept in a temporary file until Save.
	Staged bool `json:"staged,omitempty"`
	// Markers are the track start and end positions inside the side.
	Markers []Marker `json:"markers,omitempty"`
	// Listing is the side's looked-up track listing, used by Split by tracklist.
	Listing []ListingTrack `json:"listing,omitempty"`
}

// Leftover is an unsaved take kept in the staging folder, for example after a crash.
type Leftover struct {
	Side       string `json:"side"`
	Bytes      uint64 `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
}

// Info is a snapshot of a project for the UI. Name and cover are the staged
// values when there are unsaved edits.
type Info struct {
	Dir          string          `json:"dir"`
	Name         string          `json:"name"`
	Cover        string          `json:"cover"`
	CoverMissing bool            `json:"coverMissing"`
	CoverDraft   bool            `json:"coverDraft"`
	Artist       string          `json:"artist"`
	Compilation  bool            `json:"compilation"`
	Year         string          `json:"year"`
	Album        string          `json:"album"`
	Genre        string          `json:"genre"`
	Dirty        bool            `json:"dirty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Sides        map[string]Side `json:"sides"`
	Leftovers    []Leftover      `json:"leftovers"`
}

// Details describes the cassette. When Compilation is true the Artist text is
// kept but counts as absent.
type Details struct {
	Artist      string
	Compilation bool
	Year        string
	Album       string
	Genre       string
}

const maxGenreLen = 100

const maxTextLen = 200

func checkText(label i18n.Key, v string) error {
	if utf8.RuneCountInString(v) > maxTextLen {
		return i18n.New("project.mustBeAtMostCharacters", label, maxTextLen)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return i18n.New("project.containsInvalidCharacters", label)
		}
	}
	return nil
}

func repairText(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	if r := []rune(v); len(r) > maxTextLen {
		v = string(r[:maxTextLen])
	}
	return strings.TrimSpace(v)
}

// normalizeDetails trims and validates details; strict=false repairs invalid
// stored values instead of rejecting them.
func normalizeDetails(d Details, strict bool) (Details, error) {
	d.Artist = strings.TrimSpace(d.Artist)
	d.Album = strings.TrimSpace(d.Album)
	d.Year = strings.TrimSpace(d.Year)
	d.Genre = strings.TrimSpace(d.Genre)
	if strict {
		if utf8.RuneCountInString(d.Genre) > maxGenreLen {
			return d, i18n.New("project.mustBeAtMostCharacters", i18n.Key("project.label.genre"), maxGenreLen)
		}
		if err := checkText(i18n.Key("project.label.genre"), d.Genre); err != nil {
			return d, err
		}
		if err := checkText(i18n.Key("project.label.artist"), d.Artist); err != nil {
			return d, err
		}
		if err := checkText(i18n.Key("project.label.album"), d.Album); err != nil {
			return d, err
		}
		if !validYear(d.Year) {
			return d, i18n.New("project.theYearMustBeEmpty")
		}
		return d, nil
	}
	d.Artist, d.Album, d.Genre = repairText(d.Artist), repairText(d.Album), repairText(d.Genre)
	if r := []rune(d.Genre); len(r) > maxGenreLen {
		d.Genre = strings.TrimSpace(string(r[:maxGenreLen]))
	}
	if !validYear(d.Year) {
		d.Year = ""
	}
	return d, nil
}

func validYear(y string) bool {
	if y == "" {
		return true
	}
	if len(y) != 4 {
		return false
	}
	n := 0
	for _, r := range y {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n >= 1900 && n <= 2100
}

type coverDraft struct {
	data  []byte
	ext   string
	clear bool
}

// Project is an open project. It is safe for concurrent use.
type Project struct {
	dir string
	mu  sync.Mutex
	raw map[string]json.RawMessage // keeps unknown descriptor fields

	name      string
	cover     string
	createdAt time.Time
	updatedAt time.Time
	sides     map[string]Side

	details      Details
	draftDetails *Details
	draftName    *string
	// staged holds finished recordings and imports waiting for Save; their audio
	// lives in the staging folder and the project's side files are untouched.
	staged map[string]Side
	// deleted marks sides removed until Save; their files stay on disk until then.
	deleted map[string]bool
	// descStale is set when Save moved side files but could not write the descriptor.
	descStale  bool
	draftCover *coverDraft
	// draftMarkers holds marker edits per side until Save.
	draftMarkers map[string][]Marker
	// draftListing holds track listing changes per side until Save.
	draftListing map[string][]ListingTrack
	// historyTouch orders history activity across sides.
	historyTouch int64
	// history is the per-side undo/redo list of editor changes.
	history map[string]*sideHistory
}

// ValidateName trims and checks a project name.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", i18n.New("project.theProjectNameMustBe", maxNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", i18n.New("project.theProjectNameContainsInvalid")
		}
	}
	return name, nil
}

// SanitizeDirName converts a project name into a safe directory name.
func SanitizeDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(strings.TrimSpace(b.String()), ". ")
	if s == "" {
		return "tape2digi-project"
	}
	return s
}

// checkTarget requires dir to be new or empty and reports whether it did not exist.
func checkTarget(dir string) (created bool, err error) {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil:
		if len(entries) > 0 {
			return false, i18n.New("project.folderAlreadyExistsAndIs", dir)
		}
		return false, nil
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	default:
		return false, err
	}
}

// Create makes dir itself the project folder with a new descriptor; no subfolder
// is added. The folder must be new or empty.
func Create(dir, name string) (*Project, error) {
	name, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	if _, err := checkTarget(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	p := &Project{dir: dir, raw: map[string]json.RawMessage{}, name: name, createdAt: now, updatedAt: now}
	if err := p.save(); err != nil {
		return nil, err
	}
	return p, nil
}

type descriptor struct {
	SchemaVersion int             `json:"schemaVersion"`
	Name          string          `json:"name"`
	Cover         *string         `json:"cover"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Sides         map[string]Side `json:"sides"`
	Artist        string          `json:"artist"`
	Compilation   bool            `json:"compilation"`
	Year          string          `json:"year"`
	Album         string          `json:"album"`
	Genre         string          `json:"genre"`
}

// Open reads and validates the descriptor in dir. Version 1 projects open
// with no sides; their old recordings field is kept untouched in the file.
func Open(dir string) (*Project, error) {
	path := filepath.Join(dir, DescriptorName)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, i18n.New("project.theFolderHasNoFile", DescriptorName)
		}
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, i18n.New("project.isDamaged", DescriptorName, err)
	}
	var d descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, i18n.New("project.isDamaged", DescriptorName, err)
	}
	switch {
	case d.SchemaVersion > SchemaVersion:
		return nil, i18n.New("project.theProjectWasCreatedBy")
	case d.SchemaVersion < 1:
		return nil, i18n.New("project.unsupportedProjectVersion")
	}
	det, _ := normalizeDetails(Details{Artist: d.Artist, Compilation: d.Compilation, Year: d.Year, Album: d.Album, Genre: d.Genre}, false)
	removeStaleVersions(dir)
	p := &Project{dir: dir, raw: raw, name: d.Name, createdAt: d.CreatedAt, updatedAt: d.UpdatedAt, sides: map[string]Side{}, details: det}
	for k, s := range d.Sides {
		if ValidSide(k) {
			s.Markers = cleanMarkers(s.Markers, s.DurationMs)
			p.sides[k] = s
		}
	}
	if d.Cover != nil {
		p.cover = *d.Cover
	}
	if strings.TrimSpace(p.name) == "" {
		p.name = filepath.Base(dir)
	}
	return p, nil
}

// Dir returns the project directory.
func (p *Project) Dir() string { return p.dir }

// Name returns the effective project name (staged when there is an unsaved edit).
func (p *Project) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveNameLocked()
}

func (p *Project) effectiveNameLocked() string {
	if p.draftName != nil {
		return *p.draftName
	}
	return p.name
}

// Dirty reports whether name or cover edits are waiting for Save.
func (p *Project) Dirty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirtyLocked()
}

func (p *Project) dirtyLocked() bool {
	return p.draftName != nil || p.draftCover != nil || p.draftDetails != nil || len(p.staged) > 0 || len(p.draftMarkers) > 0 || len(p.draftListing) > 0 || len(p.deleted) > 0 || p.descStale
}

// SidePath returns the absolute path of a side's final file.
func (p *Project) SidePath(key string) (string, error) {
	if !ValidSide(key) {
		return "", i18n.New("project.unknownSide", key)
	}
	return filepath.Join(p.dir, SideFileName(key)), nil
}

// StagingDir is the hidden folder holding takes that Save has not yet applied.
const StagingDir = ".tape2digi-staging"

func (p *Project) stagingPath(key, ext string) (string, error) {
	if !ValidSide(key) {
		return "", i18n.New("project.unknownSide", key)
	}
	return filepath.Join(p.dir, StagingDir, "side-"+key+ext), nil
}

// PartPath returns the temporary file an in-progress take or import writes to.
func (p *Project) PartPath(key string) (string, error) { return p.stagingPath(key, ".rec") }

// stagedPath is where a finished, unsaved side is kept.
func (p *Project) stagedPath(key string) (string, error) { return p.stagingPath(key, ".wav") }

// AudioPath returns the file holding the side's current audio: the pending
// temporary file when the side is staged, otherwise the project's side file.
func (p *Project) AudioPath(key string) (string, error) {
	p.mu.Lock()
	_, st := p.staged[key]
	gone := p.deleted[key]
	p.mu.Unlock()
	if gone {
		return "", i18n.New("project.sideHasNoAudio", key)
	}
	if st {
		return p.stagedPath(key)
	}
	return p.SidePath(key)
}

// SideExists reports whether the side already has audio: it is staged, listed
// in the descriptor, or its file is on disk (for example copied in by hand).
func (p *Project) SideExists(key string) bool {
	p.mu.Lock()
	_, ok := p.sides[key]
	_, st := p.staged[key]
	gone := p.deleted[key]
	p.mu.Unlock()
	if gone {
		return false
	}
	if ok || st {
		return true
	}
	path, err := p.SidePath(key)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// leftoverPath is the unsaved audio of a side that this session does not track:
// an interrupted take, or a finished one orphaned by a crash.
func (p *Project) leftoverPath(key string) string {
	rec, err := p.PartPath(key)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(rec); err == nil {
		return rec
	}
	p.mu.Lock()
	_, tracked := p.staged[key]
	p.mu.Unlock()
	if tracked {
		return ""
	}
	if w, _ := p.stagedPath(key); w != "" {
		if _, err := os.Stat(w); err == nil {
			return w
		}
	}
	return ""
}

// HasPart reports whether an unsaved take left over from an earlier session or
// crash exists for the side.
func (p *Project) HasPart(key string) bool { return p.leftoverPath(key) != "" }

// LeftoverPath returns the file of that leftover take, or "".
func (p *Project) LeftoverPath(key string) string { return p.leftoverPath(key) }

// RemovePart deletes the side's leftover take.
func (p *Project) RemovePart(key string) error {
	path := p.leftoverPath(key)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Leftovers lists unsaved takes with the audio they contain.
func (p *Project) Leftovers() []Leftover {
	out := []Leftover{}
	for _, key := range SideKeys {
		path := p.leftoverPath(key)
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		l := Leftover{Side: key}
		if st, err := f.Stat(); err == nil {
			if format, data, err := wav.Inspect(f, st.Size()); err == nil {
				l.Bytes = data
				l.DurationMs = int64(data * 1000 / uint64(format.SampleRate*format.BlockAlign()))
			}
		}
		f.Close()
		out = append(out, l)
	}
	return out
}

// StageSide moves a finished temporary take into the staging folder as the
// side's pending audio. The project's own side file and descriptor stay as
// they are until Save. When the side already had a recording the change is one
// undoable step that brings back the previous audio, markers and listing.
func (p *Project) StageSide(key, part string, s Side) error {
	dst, err := p.stagedPath(key)
	if err != nil {
		return err
	}
	sidePath, err := p.SidePath(key)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(sidePath)
	s.File = SideFileName(key)
	s.Missing = false
	s.Staged = true
	s.Markers = nil
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.Listing == nil {
		s.Listing = p.effectiveListingLocked(key)
	}
	_, saved := p.sides[key]
	_, staged := p.staged[key]
	replacing := (saved || staged || p.deleted[key] || statErr == nil) && part != dst
	var prev sideState
	h := p.historyLocked(key)
	if replacing {
		if prev, err = p.captureStateLocked(key, h); err != nil {
			return err
		}
	} else {
		p.clearHistoryLocked(key)
	}
	if part != dst {
		if err := os.Rename(part, dst); err != nil {
			if replacing {
				p.putBackLocked(key, prev)
			}
			return err
		}
	}
	delete(p.draftMarkers, key)
	delete(p.draftListing, key)
	delete(p.deleted, key)
	if p.staged == nil {
		p.staged = map[string]Side{}
	}
	p.staged[key] = s
	if replacing {
		p.pushLocked(key, step{replace: true, other: &prev})
	}
	return nil
}

// SetSide registers a finished side and persists the descriptor. Staged name
// and cover edits are not written.
func (p *Project) SetSide(key string, s Side) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	s.File = SideFileName(key)
	s.Missing = false
	s.Markers = nil
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.Listing == nil {
		s.Listing = p.effectiveListingLocked(key)
	}
	delete(p.draftMarkers, key)
	delete(p.draftListing, key)
	p.clearHistoryLocked(key)
	if p.sides == nil {
		p.sides = map[string]Side{}
	}
	old, had := p.sides[key]
	p.sides[key] = s
	if err := p.saveLocked(); err != nil {
		if had {
			p.sides[key] = old
		} else {
			delete(p.sides, key)
		}
		return err
	}
	return nil
}

// Info returns a snapshot, flagging missing files.
func (p *Project) Info() Info {
	p.mu.Lock()
	info := Info{
		Dir: p.dir, Name: p.effectiveNameLocked(), Cover: p.cover, Dirty: p.dirtyLocked(),
		CreatedAt: p.createdAt, UpdatedAt: p.updatedAt, Sides: map[string]Side{},
	}
	det := p.effectiveDetailsLocked()
	info.Artist, info.Compilation, info.Year, info.Album, info.Genre = det.Artist, det.Compilation, det.Year, det.Album, det.Genre
	switch d := p.draftCover; {
	case d != nil && d.clear:
		info.Cover = ""
	case d != nil:
		info.Cover, info.CoverDraft = "cover"+d.ext, true
	case p.cover != "":
		if _, err := os.Stat(filepath.Join(p.dir, p.cover)); err != nil {
			info.CoverMissing = true
		}
	}
	for k, s := range p.sides {
		if p.deleted[k] {
			continue
		}
		if _, err := os.Stat(filepath.Join(p.dir, filepath.FromSlash(s.File))); err != nil {
			s.Missing = true
		}
		info.Sides[k] = p.withDraftsLocked(k, s)
	}
	for k, s := range p.staged {
		if w, _ := p.stagedPath(k); w != "" {
			if _, err := os.Stat(w); err != nil {
				s.Missing = true
			}
		}
		info.Sides[k] = p.withDraftsLocked(k, s)
	}
	p.mu.Unlock()
	info.Leftovers = p.Leftovers()
	return info
}

// Rename stages a new project name; it is written by Save.
func (p *Project) Rename(name string) error {
	name, err := ValidateName(name)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == p.name {
		p.draftName = nil
	} else {
		p.draftName = &name
	}
	return nil
}

func (p *Project) effectiveDetailsLocked() Details {
	if p.draftDetails != nil {
		return *p.draftDetails
	}
	return p.details
}

// SetDetails stages the artist, compilation flag, year, album and genre; Save writes them.
// An invalid value changes nothing.
func (p *Project) SetDetails(artist string, compilation bool, year, album, genre string) error {
	return p.stageDetails(func(d *Details) {
		d.Artist, d.Compilation, d.Year, d.Album, d.Genre = artist, compilation, year, album, genre
	})
}

func (p *Project) stageDetails(change func(*Details)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.effectiveDetailsLocked()
	was := d.Compilation
	change(&d)
	d, err := normalizeDetails(d, true)
	if err != nil {
		return err
	}
	if d == p.details {
		p.draftDetails = nil
	} else {
		p.draftDetails = &d
	}
	if was != d.Compilation {
		p.pushLocked(DetailsKey, step{details: true, compBefore: was, compAfter: d.Compilation})
	}
	return nil
}

// SetCover validates a PNG or JPEG and stages it as the cover; it is copied
// into the project as cover.<ext> by Save.
func (p *Project) SetCover(src string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.Size() > MaxCoverBytes {
		return i18n.New("project.theImageIsTooLarge")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return p.SetCoverData(data)
}

// SetCoverData stages PNG or JPEG bytes as the cover, like SetCover.
func (p *Project) SetCoverData(data []byte) error {
	if len(data) > MaxCoverBytes {
		return i18n.New("project.theImageIsTooLarge")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return i18n.New("project.theCoverMustBeA")
	}
	ext := ".png"
	if format == "jpeg" {
		ext = ".jpg"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.draftCover = &coverDraft{data: data, ext: ext}
	return nil
}

// ClearCover stages the removal of the cover.
func (p *Project) ClearCover() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cover == "" {
		p.draftCover = nil
	} else {
		p.draftCover = &coverDraft{clear: true}
	}
	return nil
}

// Discard drops staged name, cover and side edits and deletes their temporary files.
func (p *Project) Discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.draftName, p.draftCover, p.draftDetails, p.descStale = nil, nil, nil, false
	p.draftMarkers, p.draftListing, p.deleted = nil, nil, nil
	p.clearAllHistoryLocked()
	for k := range p.staged {
		if w, err := p.stagedPath(k); err == nil {
			_ = os.Remove(w)
		}
	}
	p.staged = nil
}

// CoverData returns the effective cover bytes and content type, or nil when there is none.
func (p *Project) CoverData() ([]byte, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := p.draftCover; d != nil {
		if d.clear {
			return nil, ""
		}
		return d.data, contentType(d.ext)
	}
	if p.cover == "" {
		return nil, ""
	}
	data, err := os.ReadFile(filepath.Join(p.dir, p.cover))
	if err != nil {
		return nil, ""
	}
	return data, contentType(filepath.Ext(p.cover))
}

func contentType(ext string) string {
	if ext == ".jpg" {
		return "image/jpeg"
	}
	return "image/png"
}

// Save applies staged name, cover and side edits: pending side audio replaces
// the project's side files and the descriptor is rewritten.
func (p *Project) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	oldName, oldCover, oldDetails := p.name, p.cover, p.details
	if p.draftName != nil {
		p.name = *p.draftName
	}
	if p.draftDetails != nil {
		p.details = *p.draftDetails
	}
	var stale []string
	if d := p.draftCover; d != nil {
		if d.clear {
			p.cover = ""
		} else {
			name := "cover" + d.ext
			if err := appdata.WriteFileAtomic(filepath.Join(p.dir, name), d.data, 0o644); err != nil {
				p.name, p.cover, p.details = oldName, oldCover, oldDetails
				return err
			}
			p.cover = name
		}
		stale = []string{"cover.png", "cover.jpg"}
	}
	moved := false
	for k, st := range p.staged {
		w, _ := p.stagedPath(k)
		final := filepath.Join(p.dir, SideFileName(k))
		if err := os.Rename(w, final); err != nil {
			if moved {
				p.descStale = true
			}
			p.name, p.cover, p.details = oldName, oldCover, oldDetails
			return i18n.New("project.couldNotSaveSide", k, err)
		}
		moved = true
		st.Staged = false
		if p.sides == nil {
			p.sides = map[string]Side{}
		}
		p.sides[k] = st
		delete(p.staged, k)
	}
	oldSides := map[string]Side{}
	var removed []string
	for k := range p.deleted {
		if s, ok := p.sides[k]; ok {
			oldSides[k] = s
			delete(p.sides, k)
			removed = append(removed, s.File)
		}
	}
	for k, s := range p.sides {
		if _, ok := p.draftMarkers[k]; !ok {
			if _, ok := p.draftListing[k]; !ok {
				continue
			}
		}
		oldSides[k] = s
		p.sides[k] = p.withDraftsLocked(k, s)
	}
	if err := p.saveLocked(); err != nil {
		for k, s := range oldSides {
			p.sides[k] = s
		}
		p.name, p.cover, p.details = oldName, oldCover, oldDetails
		if moved {
			p.descStale = true
		}
		return err
	}
	for _, f := range removed {
		_ = os.Remove(filepath.Join(p.dir, filepath.FromSlash(f)))
	}
	for _, e := range stale {
		if e != p.cover {
			_ = os.Remove(filepath.Join(p.dir, e))
		}
	}
	p.draftName, p.draftCover, p.draftDetails, p.descStale = nil, nil, nil, false
	p.draftMarkers, p.draftListing, p.deleted = nil, nil, nil
	p.clearAllHistoryLocked()
	p.staged = nil
	return nil
}

// SaveAs copies the project, with staged edits applied, into <parent>/<name>/
// and returns the copy. The original keeps its last saved state. progress
// receives bytes copied and the total; cancelling ctx removes the partial copy.
func (p *Project) SaveAs(ctx context.Context, parent, name string, progress func(done, total int64)) (*Project, error) {
	name, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	dir := parent
	if filepath.Clean(dir) == filepath.Clean(p.dir) {
		return nil, i18n.New("project.chooseADifferentFolderFor")
	}
	created, err := checkTarget(dir)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	sides := map[string]Side{}
	type copyJob struct{ src, rel string }
	var files []copyJob
	var total int64
	for k, s := range p.sides {
		if _, pending := p.staged[k]; pending || p.deleted[k] {
			continue
		}
		src := filepath.Join(p.dir, filepath.FromSlash(s.File))
		if st, err := os.Stat(src); err == nil {
			sides[k] = p.withDraftsLocked(k, s)
			files = append(files, copyJob{src, s.File})
			total += st.Size()
		}
	}
	for k, s := range p.staged {
		w, _ := p.stagedPath(k)
		if st, err := os.Stat(w); err == nil {
			s.Staged = false
			sides[k] = p.withDraftsLocked(k, s)
			files = append(files, copyJob{w, s.File})
			total += st.Size()
		}
	}
	cover, coverData := p.cover, []byte(nil)
	if d := p.draftCover; d != nil {
		if d.clear {
			cover = ""
		} else {
			cover, coverData = "cover"+d.ext, d.data
		}
	}
	if cover != "" && coverData == nil {
		if st, err := os.Stat(filepath.Join(p.dir, cover)); err == nil {
			files = append(files, copyJob{filepath.Join(p.dir, cover), cover})
			total += st.Size()
		} else {
			cover = ""
		}
	}
	total += int64(len(coverData))
	raw := make(map[string]json.RawMessage, len(p.raw))
	for k, v := range p.raw {
		raw[k] = v
	}
	createdAt := p.createdAt
	details := p.effectiveDetailsLocked()
	p.mu.Unlock()

	var written []string
	cleanup := func() {
		if created {
			_ = os.RemoveAll(dir)
			return
		}
		for _, f := range written {
			_ = os.Remove(f)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var done int64
	report := func(n int64) {
		done += n
		if progress != nil {
			progress(done, total)
		}
	}
	for _, j := range files {
		dst := filepath.Join(dir, filepath.FromSlash(j.rel))
		written = append(written, dst)
		if err := copyFile(ctx, dst, j.src, report); err != nil {
			cleanup()
			return nil, err
		}
	}
	if coverData != nil {
		dst := filepath.Join(dir, cover)
		written = append(written, dst)
		if err := appdata.WriteFileAtomic(dst, coverData, 0o644); err != nil {
			cleanup()
			return nil, err
		}
		report(int64(len(coverData)))
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}
	np := &Project{dir: dir, raw: raw, name: name, cover: cover, createdAt: createdAt, sides: sides, details: details}
	if err := np.save(); err != nil {
		cleanup()
		return nil, err
	}
	return np, nil
}

func copyFile(ctx context.Context, dst, src string, report func(int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			report(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}

func (p *Project) save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked()
}

// removedKeys are descriptor fields of features that no longer exist; Save drops them.
var removedKeys = []string{"notes", "tapeBrand", "tapeType", "noiseReduction"}

// saveLocked writes the saved (not staged) state.
func (p *Project) saveLocked() error {
	p.updatedAt = time.Now().UTC().Truncate(time.Second)
	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		p.raw[key] = b
		return nil
	}
	var cover *string
	if p.cover != "" {
		cover = &p.cover
	}
	sides := p.sides
	if sides == nil {
		sides = map[string]Side{}
	}
	for k, v := range map[string]any{
		"schemaVersion": SchemaVersion, "name": p.name, "cover": cover,
		"createdAt": p.createdAt, "updatedAt": p.updatedAt, "sides": sides,
		"artist": p.details.Artist, "compilation": p.details.Compilation, "year": p.details.Year, "album": p.details.Album, "genre": p.details.Genre,
	} {
		if err := set(k, v); err != nil {
			return err
		}
	}
	for _, k := range removedKeys {
		delete(p.raw, k)
	}
	b, err := json.MarshalIndent(p.raw, "", "  ")
	if err != nil {
		return err
	}
	return appdata.WriteFileAtomic(filepath.Join(p.dir, DescriptorName), append(b, '\n'), 0o644)
}

// RemoveSide drops a side from the descriptor without touching its file.
func (p *Project) RemoveSide(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sides, key)
	delete(p.draftMarkers, key)
	delete(p.draftListing, key)
	p.clearHistoryLocked(key)
	return p.saveLocked()
}
