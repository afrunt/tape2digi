// Package settings persists the user's recording preferences.
package settings

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/afrunt/tape2digi/internal/appdata"
)

const fileName = "settings.json"

// Allowed UI text size range in percent.
const (
	MinFontScale = 100
	MaxFontScale = 150
)

// Settings are restored on the next launch. Monitor is deliberately absent:
// it is off at every start.
type Settings struct {
	DeviceName string `json:"deviceName"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
	BitDepth   int    `json:"bitDepth"`
	// MonitorDeviceName is the chosen monitor output; empty follows the
	// system default output.
	MonitorDeviceName string `json:"monitorDeviceName"`
	// Language is the UI language, "en" or "uk"; anything else means "en".
	Language string `json:"language"`
	// Theme is the UI theme, "dark" or "light"; anything else means "dark".
	Theme string `json:"theme"`
	// FontScale is the UI text size in percent, 100 to 150; anything else is
	// clamped, and a missing value means 100.
	FontScale int `json:"fontScale"`
	// Window is the main window as it was left; nil means use the defaults.
	Window *Window `json:"window,omitempty"`
	// EditorWindow is the side editor window inside the main window; nil means
	// the default centred layout.
	EditorWindow *Rect `json:"editorWindow,omitempty"`
	// Export holds the last export choices; nil means the defaults.
	Export *ExportPrefs `json:"export,omitempty"`
	// AutoStop ends a take after AutoStopSec seconds of end-of-tape silence.
	AutoStop    bool `json:"autoStop"`
	AutoStopSec int  `json:"autoStopSec"`
	// OnlineLookup allows sending the artist and album text to MusicBrainz.
	OnlineLookup bool `json:"onlineLookup"`
	// ExportPresets are named export choices (without the target folder).
	ExportPresets []ExportPreset `json:"exportPresets,omitempty"`
	// LastExportDir and LastProjectsDir are where the folder pickers open
	// next time; empty means the system default.
	LastExportDir   string `json:"lastExportDir,omitempty"`
	LastProjectsDir string `json:"lastProjectsDir,omitempty"`
}

// Folder picker purposes.
const (
	FolderExport        = "export"
	FolderProjectParent = "projectParent"
	FolderProject       = "project"
)

// StartFolder is the remembered folder a picker for purpose opens in; ""
// for an unknown purpose or when nothing is remembered yet.
func (s Settings) StartFolder(purpose string) string {
	switch purpose {
	case FolderExport:
		return s.LastExportDir
	case FolderProjectParent, FolderProject:
		return s.LastProjectsDir
	}
	return ""
}

// RememberFolder records the folder chosen for purpose and reports whether
// anything changed. A chosen project folder remembers its parent, so the
// next picker opens next to it.
func (s *Settings) RememberFolder(purpose, chosen string) bool {
	if chosen == "" {
		return false
	}
	var dst *string
	switch purpose {
	case FolderExport:
		dst = &s.LastExportDir
	case FolderProjectParent:
		dst = &s.LastProjectsDir
	case FolderProject:
		dst, chosen = &s.LastProjectsDir, filepath.Dir(chosen)
	default:
		return false
	}
	if *dst == chosen {
		return false
	}
	*dst = chosen
	return true
}

// ExportPreset is a named set of export choices.
type ExportPreset struct {
	Name  string      `json:"name"`
	Prefs ExportPrefs `json:"prefs"`
}

// Preset limits.
const (
	MaxPresets       = 20
	MaxPresetNameLen = 50
)

// PresetName trims a preset name and reports whether it is usable.
func PresetName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= MaxPresetNameLen
}

// Allowed auto-stop interval in seconds.
const (
	MinAutoStopSec = 10
	MaxAutoStopSec = 600
)

// ExportPrefs are the export dialog choices that are remembered.
type ExportPrefs struct {
	Format     string  `json:"format"`
	Bitrate    int     `json:"bitrate"`
	WAVRate    int     `json:"wavRate"`    // 0 keeps the recording rate
	CoverMaxMB float64 `json:"coverMaxMB"` // limit for the cover in MP3 and WAV tags
	EmbedCover bool    `json:"embedCover"`
	// ExportOriginal adds the original side recordings; off by default.
	ExportOriginal bool `json:"exportOriginal"`
	// ReplayGain writes loudness tags; on by default.
	ReplayGain bool `json:"replayGain"`
	// NameTemplate names the track files ("{n} - {title}" by default).
	NameTemplate string `json:"nameTemplate"`
}

// DefaultNameTemplate matches the exporter default; settings do not import
// the exporter.
const DefaultNameTemplate = "{n} - {title}"

// DefaultExport is used until the user exports for the first time.
func DefaultExport() ExportPrefs {
	return ExportPrefs{Format: "mp3", Bitrate: 320, CoverMaxMB: 1, EmbedCover: true, ReplayGain: true, NameTemplate: DefaultNameTemplate}
}

// UnmarshalJSON keeps the defaults of fields that older settings lack.
func (e *ExportPrefs) UnmarshalJSON(b []byte) error {
	type plain ExportPrefs
	p := plain{ReplayGain: true}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.NameTemplate == "" {
		p.NameTemplate = DefaultNameTemplate
	}
	*e = ExportPrefs(p)
	return nil
}

// Minimum main window size, matching the window options.
const (
	MinWindowWidth  = 860
	MinWindowHeight = 600
)

// Rect is a position and size in whole pixels.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Window is the main window geometry. Rect holds the last normal (not
// maximised) geometry.
type Window struct {
	Rect
	Maximised bool `json:"maximised"`
}

// Visible reports whether a window at r still has a usable part on screen.
// Wails gives screen sizes but not their origins, so the screens are treated
// as one row starting at 0 and the check stays generous: at least 100 px of
// the window's width and its title bar must fall inside that area.
func Visible(r Rect, screenWidths []int, maxHeight int) bool {
	total := 0
	for _, w := range screenWidths {
		total += w
	}
	const keep = 100
	return r.X+r.W >= keep && r.X <= total-keep && r.Y >= -r.H+40 && r.Y <= maxHeight-40
}

// Defaults are used when nothing valid is stored.
func Defaults() Settings {
	return Settings{SampleRate: 44100, Channels: 2, BitDepth: 16, Language: "en", Theme: "dark", FontScale: 100, AutoStop: true, AutoStopSec: 60}
}

// Normalize replaces unsupported values with defaults.
func (s Settings) Normalize() Settings {
	d := Defaults()
	switch s.SampleRate {
	case 44100, 48000, 96000:
	default:
		s.SampleRate = d.SampleRate
	}
	if s.Channels != 1 && s.Channels != 2 {
		s.Channels = d.Channels
	}
	if s.BitDepth != 16 && s.BitDepth != 24 {
		s.BitDepth = d.BitDepth
	}
	if s.Language != "en" && s.Language != "uk" {
		s.Language = d.Language
	}
	if s.Theme != "dark" && s.Theme != "light" {
		s.Theme = d.Theme
	}
	if s.FontScale < MinFontScale {
		s.FontScale = MinFontScale
	}
	if s.FontScale > MaxFontScale {
		s.FontScale = MaxFontScale
	}
	s.AutoStopSec = min(max(s.AutoStopSec, MinAutoStopSec), MaxAutoStopSec)
	if s.Window != nil && (s.Window.W < MinWindowWidth || s.Window.H < MinWindowHeight) {
		s.Window.W = max(s.Window.W, MinWindowWidth)
		s.Window.H = max(s.Window.H, MinWindowHeight)
	}
	if s.EditorWindow != nil && (s.EditorWindow.W <= 0 || s.EditorWindow.H <= 0) {
		s.EditorWindow = nil
	}
	if s.Export != nil {
		e := s.Export.Normalize()
		s.Export = &e
	}
	// Presets: usable names only, unique ignoring case, at most MaxPresets.
	var presets []ExportPreset
	seen := map[string]bool{}
	for _, p := range s.ExportPresets {
		name, ok := PresetName(p.Name)
		if !ok || seen[strings.ToLower(name)] || len(presets) == MaxPresets {
			continue
		}
		seen[strings.ToLower(name)] = true
		presets = append(presets, ExportPreset{Name: name, Prefs: p.Prefs.Normalize()})
	}
	s.ExportPresets = presets
	return s
}

// Normalize replaces unsupported export choices with defaults.
func (e ExportPrefs) Normalize() ExportPrefs {
	d := DefaultExport()
	if e.Format != "wav" && e.Format != "mp3" && e.Format != "flac-cue" {
		e.Format = d.Format
	}
	if e.Bitrate != 128 && e.Bitrate != 192 && e.Bitrate != 256 && e.Bitrate != 320 {
		e.Bitrate = d.Bitrate
	}
	if e.WAVRate != 0 && e.WAVRate != 44100 && e.WAVRate != 48000 && e.WAVRate != 88200 && e.WAVRate != 96000 {
		e.WAVRate = 0
	}
	if !(e.CoverMaxMB > 0 && e.CoverMaxMB <= 50) {
		e.CoverMaxMB = d.CoverMaxMB
	}
	if e.NameTemplate = strings.TrimSpace(e.NameTemplate); e.NameTemplate == "" || utf8.RuneCountInString(e.NameTemplate) > 100 {
		e.NameTemplate = d.NameTemplate
	}
	return e
}

// Store loads and saves Settings in an app data directory.
type Store struct{ dir appdata.Dir }

// NewStore creates a store backed by dir.
func NewStore(dir appdata.Dir) *Store { return &Store{dir: dir} }

// Load returns stored settings, or defaults when the file is missing or corrupt.
func (s *Store) Load() Settings {
	// Fields missing from an older file keep their defaults.
	v := Defaults()
	if err := appdata.ReadJSON(s.dir.File(fileName), &v); err != nil {
		return Defaults()
	}
	return v.Normalize()
}

// Save persists settings.
func (s *Store) Save(v Settings) error {
	if err := s.dir.Ensure(); err != nil {
		return err
	}
	return appdata.WriteJSON(s.dir.File(fileName), v.Normalize())
}
