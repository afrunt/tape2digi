package main

import (
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/afrunt/tape2digi/internal/appdata"
	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/recorder"
	"github.com/afrunt/tape2digi/internal/settings"
)

func TestCoverHandlerServesOnlyCurrentCover(t *testing.T) {
	app := &App{}
	get := func(path string) int {
		rec := httptest.NewRecorder()
		coverHandler{app}.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/cover") != http.StatusNotFound {
		t.Fatal("no project: expected 404")
	}

	p, err := project.Create(t.TempDir(), "A")
	if err != nil {
		t.Fatal(err)
	}
	app.proj = p
	if get("/cover") != http.StatusNotFound {
		t.Fatal("no cover: expected 404")
	}

	src := filepath.Join(t.TempDir(), "c.png")
	f, _ := os.Create(src)
	_ = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	f.Close()
	if err := p.SetCover(src); err != nil {
		t.Fatal(err)
	}
	if get("/cover") != http.StatusOK {
		t.Fatal("staged cover should be served from memory")
	}
	if _ = p.ClearCover(); get("/cover") != http.StatusNotFound {
		t.Fatal("cleared cover must 404")
	}
	_ = p.SetCover(src)
	for _, bad := range []string{"/cover/../tape2digi.json", "/tape2digi.json", "/../../etc/passwd"} {
		if get(bad) != http.StatusNotFound {
			t.Fatalf("%s must be 404", bad)
		}
	}
}

func TestMonitorOutputChoiceSurvivesOpenInput(t *testing.T) {
	src := &audio.FakeSource{
		Devs:     []audio.Device{{ID: "d1", Name: "USB", IsDefault: true}},
		PlayDevs: []audio.Device{{ID: "o1", Name: "Speakers", IsDefault: true}, {ID: "o2", Name: "Headphones"}},
	}
	dir := appdata.Dir{Path: t.TempDir()}
	app := &App{data: dir, settings: settings.NewStore(dir), src: src, rec: recorder.New(src, recorder.Handlers{})}
	if err := app.SetMonitorDevice("Headphones"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenInput("USB", 48000, 2, 24); err != nil {
		t.Fatal(err)
	}
	got := app.GetSettings()
	if got.MonitorDeviceName != "Headphones" || got.SampleRate != 48000 || got.BitDepth != 24 {
		t.Fatalf("settings lost a field: %+v", got)
	}
	if err := app.SetMonitorDevice(""); err != nil {
		t.Fatal(err)
	}
	if got := app.GetSettings(); got.MonitorDeviceName != "" || got.DeviceName != "USB" {
		t.Fatalf("settings after reset: %+v", got)
	}
}

func TestSetFontScaleKeepsOtherSettingsAndRejectsOutOfRange(t *testing.T) {
	dir := appdata.Dir{Path: t.TempDir()}
	app := &App{data: dir, settings: settings.NewStore(dir)}
	if err := app.SetTheme("light"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetFontScale(130); err != nil {
		t.Fatal(err)
	}
	if got := app.GetSettings(); got.FontScale != 130 || got.Theme != "light" {
		t.Fatalf("settings: %+v", got)
	}
	for _, v := range []int{90, 105, 160} {
		if app.SetFontScale(v) == nil {
			t.Fatalf("%d accepted", v)
		}
	}
	if got := app.GetSettings(); got.FontScale != 130 {
		t.Fatalf("rejected value changed scale: %+v", got)
	}
}

func TestSetThemeKeepsOtherSettingsAndRejectsUnknown(t *testing.T) {
	dir := appdata.Dir{Path: t.TempDir()}
	app := &App{data: dir, settings: settings.NewStore(dir)}
	if err := app.SetLanguage("uk"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.SetLanguage("en") })
	if err := app.SetTheme("light"); err != nil {
		t.Fatal(err)
	}
	if got := app.GetSettings(); got.Theme != "light" || got.Language != "uk" {
		t.Fatalf("settings: %+v", got)
	}
	if err := app.SetLanguage("en"); err != nil {
		t.Fatal(err)
	}
	if got := app.GetSettings(); got.Theme != "light" || got.Language != "en" {
		t.Fatalf("language change lost theme: %+v", got)
	}
	if err := app.SetTheme("blue"); err == nil {
		t.Fatal("unknown theme accepted")
	}
	if got := app.GetSettings(); got.Theme != "light" {
		t.Fatalf("rejected value changed theme: %+v", got)
	}
}
