package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/afrunt/tape2digi/internal/appdata"
)

func TestLoadMissingAndCorruptFallBackToDefaults(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	st := NewStore(dir)
	if got := st.Load(); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("missing: %+v", got)
	}
	_ = dir.Ensure()
	_ = os.WriteFile(dir.File("settings.json"), []byte("{not json"), 0o600)
	if got := st.Load(); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("corrupt: %+v", got)
	}
}

func TestSaveLoadRoundTripAndNormalize(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	st := NewStore(dir)
	in := Settings{DeviceName: "USB Audio", SampleRate: 44100, Channels: 1, BitDepth: 16, Language: "uk", Theme: "light", FontScale: 120, AutoStop: false, AutoStopSec: 120}
	if err := st.Save(in); err != nil {
		t.Fatal(err)
	}
	if got := st.Load(); !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v", got)
	}
	_ = st.Save(Settings{SampleRate: 12345, Channels: 9, BitDepth: 8, Language: "fr", Theme: "blue", FontScale: 300})
	if got := st.Load(); got.SampleRate != 44100 || got.Channels != 2 || got.BitDepth != 16 || got.Language != "en" || got.Theme != "dark" || got.FontScale != 150 {
		t.Fatalf("not normalized: %+v", got)
	}
}

func TestFontScaleDefaultsAndClamps(t *testing.T) {
	if got := (Settings{FontScale: 0}).Normalize().FontScale; got != 100 {
		t.Fatalf("missing value: %d", got)
	}
	if got := (Settings{FontScale: 80}).Normalize().FontScale; got != 100 {
		t.Fatalf("below minimum: %d", got)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := appdata.Dir{Path: t.TempDir()}
	_ = NewStore(dir).Save(Defaults())
	entries, _ := os.ReadDir(dir.Path)
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
}

func TestWindowNormalizeAndVisible(t *testing.T) {
	s := Settings{Window: &Window{Rect: Rect{X: 10, Y: 10, W: 100, H: 100}}, EditorWindow: &Rect{W: 0, H: 50}}.Normalize()
	if s.Window.W != MinWindowWidth || s.Window.H != MinWindowHeight {
		t.Fatalf("window not raised to the minimum: %+v", s.Window)
	}
	if s.EditorWindow != nil {
		t.Fatal("empty editor window kept")
	}
	screens := []int{1920, 1440}
	cases := []struct {
		r    Rect
		want bool
	}{
		{Rect{X: 100, Y: 50, W: 1100, H: 760}, true},
		{Rect{X: 2000, Y: 0, W: 1100, H: 760}, true},
		{Rect{X: 5000, Y: 0, W: 1100, H: 760}, false},
		{Rect{X: -2000, Y: 0, W: 1100, H: 760}, false},
		{Rect{X: 100, Y: 2000, W: 1100, H: 760}, false},
	}
	for _, c := range cases {
		if got := Visible(c.r, screens, 1080); got != c.want {
			t.Errorf("Visible(%+v) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestAutoStopDefaultsAndClamps(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	_ = dir.Ensure()
	_ = os.WriteFile(dir.File("settings.json"), []byte(`{"sampleRate":48000}`), 0o600)
	if got := NewStore(dir).Load(); !got.AutoStop || got.AutoStopSec != 60 || got.SampleRate != 48000 {
		t.Fatalf("older file: %+v", got)
	}
	if got := (Settings{AutoStopSec: 5}).Normalize().AutoStopSec; got != MinAutoStopSec {
		t.Fatalf("below minimum: %d", got)
	}
	if got := (Settings{AutoStopSec: 6000}).Normalize().AutoStopSec; got != MaxAutoStopSec {
		t.Fatalf("above maximum: %d", got)
	}
}

func TestReplayGainDefaultsOnForOlderExportPrefs(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	_ = dir.Ensure()
	_ = os.WriteFile(dir.File("settings.json"), []byte(`{"export":{"format":"wav"}}`), 0o600)
	if got := NewStore(dir).Load().Export; got == nil || !got.ReplayGain || got.Format != "wav" {
		t.Fatalf("older export prefs: %+v", got)
	}
	_ = os.WriteFile(dir.File("settings.json"), []byte(`{"export":{"format":"wav","replayGain":false}}`), 0o600)
	if NewStore(dir).Load().Export.ReplayGain {
		t.Fatal("an explicit off must stay off")
	}
}

func TestExportPresetsLoadAndNormalize(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	_ = dir.Ensure()
	var list []string
	list = append(list, `{"name":"  Car  ","prefs":{"format":"mp3","bitrate":192}}`, `{"name":"car","prefs":{"format":"wav"}}`, `{"name":"","prefs":{}}`, `{"name":"`+strings.Repeat("x", 51)+`","prefs":{}}`, `{"name":"Odd","prefs":{"format":"ogg","bitrate":7}}`)
	for i := 0; i < 30; i++ {
		list = append(list, fmt.Sprintf(`{"name":"p%d","prefs":{}}`, i))
	}
	_ = os.WriteFile(dir.File("settings.json"), []byte(`{"exportPresets":[`+strings.Join(list, ",")+`]}`), 0o600)
	got := NewStore(dir).Load().ExportPresets
	if len(got) != MaxPresets {
		t.Fatalf("presets %d", len(got))
	}
	if p := got[0]; p.Name != "Car" || p.Prefs.Bitrate != 192 || !p.Prefs.ReplayGain || p.Prefs.NameTemplate != DefaultNameTemplate {
		t.Fatalf("first preset %+v", p)
	}
	if p := got[1]; p.Name != "Odd" || p.Prefs.Format != "mp3" || p.Prefs.Bitrate != 320 {
		t.Fatalf("duplicate, empty and long names are dropped and bad values reset: %+v", p)
	}
}
