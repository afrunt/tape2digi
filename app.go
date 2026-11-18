package main

import (
	"context"
	"errors"
	"github.com/afrunt/tape2digi/internal/i18n"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/afrunt/tape2digi/internal/appdata"
	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/recorder"
	"github.com/afrunt/tape2digi/internal/settings"
)

// App is the object bound to the frontend. All exported methods are callable
// from JavaScript; returned errors reach the UI as rejected promises.
type App struct {
	ctx context.Context

	data     appdata.Dir
	settings *settings.Store
	// settingsMu makes load-modify-save of settings atomic across bindings.
	settingsMu sync.Mutex
	recents    *project.Recents
	src        audio.Source
	rec        *recorder.Service
	player     *audio.Player
	playSide   string
	peakMu     sync.Mutex
	peakCache  map[string]peakEntry
	splitCache map[string]splitEntry
	envCache   map[string]envEntry

	mu         sync.Mutex
	proj       *project.Project
	job        context.CancelFunc // running import or Save As, if any
	quitOK     bool
	menuMu     sync.Mutex // guards the menu item pointers
	saveItem   *menu.MenuItem
	saveAsItem *menu.MenuItem
	newItem    *menu.MenuItem
	exportItem *menu.MenuItem
}

// ImportProgress is the payload of the "import" event.
type ImportProgress struct {
	Kind  string `json:"kind"` // "import" or "saveAs"
	Side  string `json:"side"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// QuitRequest is the payload of the "quit-request" event.
type QuitRequest struct {
	Dirty     bool `json:"dirty"`
	Recording bool `json:"recording"`
	Job       bool `json:"job"`
}

// NewApp creates the app with data under ~/.tape2digi.
func NewApp() *App {
	dir, err := appdata.Default()
	if err != nil {
		dir = appdata.Dir{Path: filepath.Join(".", appdata.DirName)}
	}
	store := settings.NewStore(dir)
	i18n.SetLocale(store.Load().Language)
	return &App{data: dir, settings: store, recents: project.NewRecents(dir)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = a.data.Ensure()
	src, err := audio.NewMalgoSource()
	if err != nil {
		a.src = unavailableSource{err}
	} else {
		a.src = src
	}
	a.rec = recorder.New(a.src, recorder.Handlers{
		OnLevel: func(l []audio.ChannelLevel) { runtime.EventsEmit(a.ctx, "level", l) },
		OnState: func(s recorder.Status) { runtime.EventsEmit(a.ctx, "state", s) },
		OnError: func(m string) { runtime.EventsEmit(a.ctx, "error", m) },
	})
	_ = a.rec.SetMonitorDevice(a.settings.Load().MonitorDeviceName)
	a.restoreWindow()
}

// screenWidths returns the widths of the connected screens and the tallest height.
func (a *App) screenWidths() (widths []int, maxH int) {
	screens, err := runtime.ScreenGetAll(a.ctx)
	if err != nil {
		return nil, 0
	}
	for _, s := range screens {
		widths = append(widths, s.Width)
		maxH = max(maxH, s.Height)
	}
	return widths, maxH
}

// restoreWindow applies the saved position and maximised state; the size was
// already used as the initial window size.
func (a *App) restoreWindow() {
	w := a.settings.Load().Window
	if w == nil {
		return
	}
	if widths, maxH := a.screenWidths(); len(widths) > 0 && settings.Visible(w.Rect, widths, maxH) {
		runtime.WindowSetPosition(a.ctx, w.X, w.Y)
	}
	if w.Maximised {
		runtime.WindowMaximise(a.ctx)
	}
}

// saveWindow remembers the main window geometry; a maximised window keeps its
// last normal geometry.
func (a *App) saveWindow() {
	if a.ctx == nil || runtime.WindowIsMinimised(a.ctx) || runtime.WindowIsFullscreen(a.ctx) {
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	win := settings.Window{}
	if cfg.Window != nil {
		win = *cfg.Window
	}
	win.Maximised = runtime.WindowIsMaximised(a.ctx)
	if !win.Maximised {
		x, y := runtime.WindowGetPosition(a.ctx)
		w, h := runtime.WindowGetSize(a.ctx)
		win.Rect = settings.Rect{X: x, Y: y, W: w, H: h}
	}
	cfg.Window = &win
	_ = a.settings.Save(cfg)
}

// GetEditorWindow returns the saved side editor window, or nil.
func (a *App) GetEditorWindow() *settings.Rect { return a.settings.Load().EditorWindow }

// SaveEditorWindow remembers the side editor window geometry.
func (a *App) SaveEditorWindow(x, y, w, h int) error {
	if w <= 0 || h <= 0 {
		return i18n.New("app.unsupportedWindow")
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	cfg.EditorWindow = &settings.Rect{X: x, Y: y, W: w, H: h}
	return a.settings.Save(cfg)
}

func (a *App) shutdown(_ context.Context) {
	a.stopPlayback()
	a.CancelJob()
	for i := 0; i < 40 && a.jobRunning(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if a.rec != nil {
		_ = a.rec.CloseInput()
	}
	if a.src != nil {
		a.src.Close()
	}
}

// ---- devices and settings ----

// Devices lists the available input devices.
func (a *App) Devices() ([]audio.Device, error) {
	devs, err := a.rec.Devices()
	if devs == nil {
		devs = []audio.Device{}
	}
	return devs, err
}

// GetSettings returns the saved recording preferences.
func (a *App) GetSettings() settings.Settings { return a.settings.Load() }

// SetLanguage persists the UI language ("en" or "uk"), switches the backend
// texts and rebuilds the native menu.
func (a *App) SetLanguage(lang string) error {
	if !i18n.Valid(lang) {
		return i18n.New("app.unsupportedLanguage")
	}
	a.settingsMu.Lock()
	cfg := a.settings.Load()
	cfg.Language = lang
	err := a.settings.Save(cfg)
	a.settingsMu.Unlock()
	if err != nil {
		return err
	}
	i18n.SetLocale(lang)
	if a.ctx != nil {
		runtime.MenuSetApplicationMenu(a.ctx, a.buildMenu())
		runtime.MenuUpdateApplicationMenu(a.ctx)
	}
	return nil
}

// SetTheme persists the UI theme ("dark" or "light").
func (a *App) SetTheme(theme string) error {
	if theme != "dark" && theme != "light" {
		return i18n.New("app.unsupportedTheme")
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	cfg.Theme = theme
	return a.settings.Save(cfg)
}

// SetFontScale persists the UI text size in percent (100 to 150, steps of 10).
func (a *App) SetFontScale(percent int) error {
	if percent < settings.MinFontScale || percent > settings.MaxFontScale || percent%10 != 0 {
		return i18n.New("app.unsupportedFontScale")
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	cfg.FontScale = percent
	return a.settings.Save(cfg)
}

// OpenInput opens the named device (empty or unknown: system default) with the
// given format, saves the choice and starts live metering. It returns the
// device that was actually opened.
func (a *App) OpenInput(deviceName string, sampleRate, channels, bitDepth int) (audio.Device, error) {
	devs, err := a.rec.Devices()
	if err != nil {
		return audio.Device{}, err
	}
	if len(devs) == 0 {
		return audio.Device{}, i18n.New("app.noInputAudioDeviceFound")
	}
	chosen := devs[0]
	found := false
	for _, d := range devs {
		if d.Name == deviceName {
			chosen, found = d, true
			break
		}
	}
	if !found {
		for _, d := range devs {
			if d.IsDefault {
				chosen = d
				break
			}
		}
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	cfg.DeviceName, cfg.SampleRate, cfg.Channels, cfg.BitDepth = chosen.Name, sampleRate, channels, bitDepth
	cfg = cfg.Normalize()
	if err := a.rec.OpenInput(chosen.ID, chosen.Name, audio.Format{SampleRate: cfg.SampleRate, Channels: cfg.Channels, BitDepth: cfg.BitDepth}); err != nil {
		return audio.Device{}, err
	}
	return chosen, a.settings.Save(cfg)
}

// CloseInput stops metering and releases the device.
func (a *App) CloseInput() error { return a.rec.CloseInput() }

// PlaybackDevices lists the output devices available for the monitor.
func (a *App) PlaybackDevices() ([]audio.Device, error) {
	devs, err := a.rec.PlaybackDevices()
	if devs == nil {
		devs = []audio.Device{}
	}
	return devs, err
}

// SetMonitorDevice chooses the monitor output by name (empty: system default)
// and remembers the choice. A running monitor moves to the new device; an
// error from opening it is returned but the choice is kept.
func (a *App) SetMonitorDevice(name string) error {
	a.settingsMu.Lock()
	cfg := a.settings.Load()
	cfg.MonitorDeviceName = name
	saveErr := a.settings.Save(cfg)
	a.settingsMu.Unlock()
	if err := a.rec.SetMonitorDevice(name); err != nil {
		return err
	}
	return saveErr
}

// SetMonitor turns audible monitoring of the input on or off.
func (a *App) SetMonitor(on bool) error {
	if on {
		a.stopPlayback()
	}
	return a.rec.SetMonitor(on)
}

// ---- recording ----

func (a *App) Status() recorder.Status { return a.rec.Status() }
func (a *App) PauseRecording() error   { return a.rec.Pause() }
func (a *App) ResumeRecording() error  { return a.rec.Resume() }

// StartRecording records a side ("A" or "B"). When the side already has audio
// it fails with the message "side_exists:<side>" unless overwrite is true.
func (a *App) StartRecording(side string, overwrite bool) error {
	if a.jobRunning() {
		return i18n.New("app.waitForTheCurrentOperation")
	}
	a.stopPlayback()
	return a.rec.Start(side, overwrite)
}

// StopRecording finalises the take and returns the updated project.
func (a *App) StopRecording() (project.Info, error) {
	_, err := a.rec.Stop()
	return a.CurrentProject(), err
}

// ImportSide converts an audio file (wav, mp3, flac, ogg) into a side.
func (a *App) ImportSide(side, path string, overwrite bool) (project.Info, error) {
	ctx, done, err := a.beginJob()
	if err != nil {
		return a.CurrentProject(), err
	}
	defer done()
	a.stopPlayback()
	_, err = a.rec.ImportSide(ctx, side, path, overwrite, func(d, t int64) {
		runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "import", Side: side, Done: d, Total: t})
	})
	return a.CurrentProject(), err
}

// RecoverPart keeps an unfinished take left by an interrupted recording.
func (a *App) RecoverPart(side string, overwrite bool) (project.Info, error) {
	_, err := a.rec.RecoverPart(side, overwrite)
	return a.CurrentProject(), err
}

// DiscardPart deletes an unfinished take.
func (a *App) DiscardPart(side string) (project.Info, error) {
	err := a.rec.DiscardPart(side)
	return a.CurrentProject(), err
}

// beginJob reserves the single long-running job slot (import or Save As).
func (a *App) beginJob() (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.job != nil || a.proj == nil {
		return nil, nil, i18n.New("app.waitForTheCurrentOperation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.job = cancel
	return ctx, func() {
		cancel()
		a.mu.Lock()
		a.job = nil
		a.mu.Unlock()
	}, nil
}

func (a *App) jobRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.job != nil
}

// CancelJob cancels a running import or Save As.
func (a *App) CancelJob() {
	a.mu.Lock()
	cancel := a.job
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ---- projects ----

// CurrentProject returns the open project (empty Dir when none).
func (a *App) CurrentProject() project.Info {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proj == nil {
		return project.Info{Sides: map[string]project.Side{}, Leftovers: []project.Leftover{}}
	}
	return a.proj.Info()
}

// CreateProject makes the chosen folder a new project and opens it.
func (a *App) CreateProject(dir, name string) (project.Info, error) {
	p, err := project.Create(dir, name)
	if err != nil {
		return project.Info{}, err
	}
	return a.activate(p)
}

// OpenProject opens an existing project directory.
func (a *App) OpenProject(dir string) (project.Info, error) {
	p, err := project.Open(dir)
	if err != nil {
		return project.Info{}, err
	}
	return a.activate(p)
}

func (a *App) activate(p *project.Project) (project.Info, error) {
	if a.jobRunning() {
		return project.Info{}, i18n.New("app.waitForTheCurrentOperation")
	}
	a.stopPlayback()
	if err := a.rec.CloseInput(); err != nil {
		return project.Info{}, err
	}
	if err := a.rec.SetProject(p); err != nil {
		return project.Info{}, err
	}
	a.mu.Lock()
	a.proj = p
	a.mu.Unlock()
	_ = a.recents.Touch(p.Dir(), p.Name())
	a.updateMenu()
	return p.Info(), nil
}

// CloseProject closes the input and returns to the start screen.
func (a *App) CloseProject() error {
	if a.jobRunning() {
		return i18n.New("app.waitForTheCurrentOperation")
	}
	a.stopPlayback()
	if err := a.rec.CloseInput(); err != nil {
		return err
	}
	if err := a.rec.SetProject(nil); err != nil {
		return err
	}
	a.mu.Lock()
	a.proj = nil
	a.mu.Unlock()
	a.updateMenu()
	return nil
}

func (a *App) withProject() (*project.Project, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proj == nil {
		return nil, i18n.New("app.noProjectIsOpen")
	}
	return a.proj, nil
}

// RenameProject stages a new project name; SaveProject writes it.
func (a *App) RenameProject(name string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	err = p.Rename(name)
	return p.Info(), err
}

// SetProjectDetails stages the artist, compilation flag, year, album and genre; SaveProject writes them.
func (a *App) SetProjectDetails(artist string, compilation bool, year, album, genre string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	err = p.SetDetails(artist, compilation, year, album, genre)
	return p.Info(), err
}

// SaveProject writes staged name and cover edits.
func (a *App) SaveProject() (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if a.jobRunning() {
		return p.Info(), i18n.New("app.waitForTheCurrentOperation")
	}
	a.stopPlayback()
	if err := p.Save(); err != nil {
		return p.Info(), err
	}
	_ = a.recents.SyncName(p.Dir(), p.Name())
	return p.Info(), nil
}

// DiscardChanges drops staged name and cover edits.
func (a *App) DiscardChanges() (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if a.jobRunning() {
		return p.Info(), i18n.New("app.waitForTheCurrentOperation")
	}
	a.stopPlayback()
	p.Discard()
	return p.Info(), nil
}

// SaveProjectAs copies the project (with staged edits) into the chosen folder and
// opens the copy. The original keeps its last saved state.
func (a *App) SaveProjectAs(parent, name string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if a.rec.Busy() {
		return p.Info(), i18n.New("app.stopRecordingOrImportingBefore")
	}
	ctx, done, err := a.beginJob()
	if err != nil {
		return p.Info(), err
	}
	a.stopPlayback()
	np, err := p.SaveAs(ctx, parent, name, func(d, t int64) {
		runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "saveAs", Done: d, Total: t})
	})
	done()
	if err != nil {
		return p.Info(), err
	}
	if err := a.rec.SetProject(np); err != nil {
		return p.Info(), err
	}
	a.mu.Lock()
	a.proj = np
	a.mu.Unlock()
	_ = a.recents.Touch(np.Dir(), np.Name())
	return np.Info(), nil
}

// ChooseAudioFile shows a native picker for audio files; it returns "" when cancelled.
func (a *App) ChooseAudioFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   i18n.T("app.dialog.importTitle"),
		Filters: []runtime.FileFilter{{DisplayName: i18n.T("app.dialog.audioFilter"), Pattern: "*.wav;*.mp3;*.flac;*.ogg"}},
	})
}

// SetCover stages the chosen PNG/JPEG as the cover; SaveProject copies it into the project.
func (a *App) SetCover(path string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	err = p.SetCover(path)
	return p.Info(), err
}

// ClearCover stages the removal of the cover.
func (a *App) ClearCover() (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	err = p.ClearCover()
	return p.Info(), err
}

// RecentProjects lists recently opened projects.
func (a *App) RecentProjects() []project.RecentEntry { return a.recents.List() }

// RemoveRecent forgets a recent project entry.
func (a *App) RemoveRecent(path string) error { return a.recents.Remove(path) }

// ---- dialogs and OS integration ----

// ChooseFolder shows a native folder picker; it returns "" when cancelled.
func (a *App) ChooseFolder(title string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title, CanCreateDirectories: true})
}

// ChooseImage shows a native picker for PNG/JPEG files; it returns "" when cancelled.
func (a *App) ChooseImage() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   i18n.T("app.dialog.coverTitle"),
		Filters: []runtime.FileFilter{{DisplayName: i18n.T("app.dialog.imageFilter"), Pattern: "*.png;*.jpg;*.jpeg"}},
	})
}

// RevealSide shows a side's WAV file in the OS file manager.
func (a *App) RevealSide(side string) error {
	p, err := a.withProject()
	if err != nil {
		return err
	}
	abs, err := p.AudioPath(side)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return i18n.New("app.theSideFileWasNot")
	}
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", abs)
	case "windows":
		cmd = exec.Command("explorer", "/select,"+abs)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(abs))
	}
	if err := cmd.Start(); err != nil {
		return i18n.New("app.couldNotOpenTheFile", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// coverHandler serves only the current project's cover at /cover; everything else is 404.
// It is a separate type so it is not exposed to the frontend as a binding.
type coverHandler struct{ a *App }

func (h coverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/cover" {
		http.NotFound(w, r)
		return
	}
	h.a.mu.Lock()
	p := h.a.proj
	h.a.mu.Unlock()
	var data []byte
	ct := ""
	if p != nil {
		data, ct = p.CoverData()
	}
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}

// unavailableSource is used when the audio backend cannot start, so the UI can
// still open and show the error.
type unavailableSource struct{ err error }

func (u unavailableSource) CaptureDevices() ([]audio.Device, error) { return nil, u.err }
func (u unavailableSource) OpenCapture(string, audio.Format, func([]byte), func()) (audio.Stream, error) {
	return nil, u.err
}
func (u unavailableSource) PlaybackDevices() ([]audio.Device, error) { return nil, u.err }
func (u unavailableSource) OpenPlayback(string, audio.Format, func([]byte), func()) (audio.Stream, error) {
	return nil, u.err
}
func (u unavailableSource) Close() {}

// ---- menu and quit ----

// buildMenu creates the application menu. Items act only while a project is open.
func (a *App) buildMenu() *menu.Menu {
	a.mu.Lock()
	open := a.proj != nil
	a.mu.Unlock()
	file := menu.NewMenu()
	a.menuMu.Lock()
	a.newItem = file.AddText(i18n.T("app.menu.new"), keys.CmdOrCtrl("n"), func(*menu.CallbackData) {
		runtime.EventsEmit(a.ctx, "menu:new")
	})
	file.AddText(i18n.T("app.menu.open"), keys.CmdOrCtrl("o"), func(*menu.CallbackData) {
		runtime.EventsEmit(a.ctx, "menu:open")
	})
	file.AddSeparator()
	a.saveItem = file.AddText(i18n.T("app.menu.save"), keys.CmdOrCtrl("s"), func(*menu.CallbackData) { a.menuSave() })
	a.saveAsItem = file.AddText(i18n.T("app.menu.saveAs"), keys.Combo("s", keys.CmdOrCtrlKey, keys.ShiftKey), func(*menu.CallbackData) {
		runtime.EventsEmit(a.ctx, "menu:saveAs")
	})
	a.exportItem = file.AddText(i18n.T("app.menu.export"), keys.CmdOrCtrl("e"), func(*menu.CallbackData) {
		runtime.EventsEmit(a.ctx, "menu:export")
	})
	a.saveItem.Disabled, a.saveAsItem.Disabled, a.newItem.Disabled, a.exportItem.Disabled = !open, !open, !open, !open
	a.menuMu.Unlock()

	root := menu.NewMenu()
	if goruntime.GOOS == "darwin" {
		root.Append(menu.AppMenu())
	}
	root.Append(menu.SubMenu(i18n.T("app.menu.file"), file))
	root.Append(menu.SubMenu(i18n.T("app.menu.edit"), a.buildEditMenu()))
	return root
}

// buildEditMenu holds Undo and Redo on every platform. The shortcuts are
// handled only here and reach the page as events, so a change never runs twice.
// macOS also needs the text commands the native Edit menu would have provided.
func (a *App) buildEditMenu() *menu.Menu {
	emit := func(name string) menu.Callback {
		return func(*menu.CallbackData) { runtime.EventsEmit(a.ctx, name) }
	}
	m := menu.NewMenu()
	m.AddText(i18n.T("app.menu.undo"), keys.CmdOrCtrl("z"), emit("menu:undo"))
	if goruntime.GOOS == "darwin" {
		m.AddText(i18n.T("app.menu.redo"), keys.Combo("z", keys.CmdOrCtrlKey, keys.ShiftKey), emit("menu:redo"))
		m.AddSeparator()
		m.AddText(i18n.T("app.menu.cut"), keys.CmdOrCtrl("x"), emit("menu:cut"))
		m.AddText(i18n.T("app.menu.copy"), keys.CmdOrCtrl("c"), emit("menu:copy"))
		m.AddText(i18n.T("app.menu.paste"), keys.CmdOrCtrl("v"), emit("menu:paste"))
		m.AddText(i18n.T("app.menu.selectAll"), keys.CmdOrCtrl("a"), emit("menu:selectAll"))
	} else {
		m.AddText(i18n.T("app.menu.redo"), keys.CmdOrCtrl("y"), emit("menu:redo"))
	}
	return m
}

func (a *App) updateMenu() {
	a.menuMu.Lock()
	if a.saveItem == nil || a.ctx == nil {
		a.menuMu.Unlock()
		return
	}
	a.mu.Lock()
	open := a.proj != nil
	a.mu.Unlock()
	a.saveItem.Disabled, a.saveAsItem.Disabled, a.newItem.Disabled, a.exportItem.Disabled = !open, !open, !open, !open
	a.menuMu.Unlock()
	runtime.MenuUpdateApplicationMenu(a.ctx)
}

func (a *App) menuSave() {
	info, err := a.SaveProject()
	if err != nil {
		runtime.EventsEmit(a.ctx, "error", err.Error())
	}
	runtime.EventsEmit(a.ctx, "project", info)
}

// beforeClose keeps the window open while there is unsaved or running work and
// asks the UI what to do.
func (a *App) beforeClose(_ context.Context) bool {
	a.saveWindow()
	a.mu.Lock()
	ok, p := a.quitOK, a.proj
	a.mu.Unlock()
	if ok {
		return false
	}
	req := QuitRequest{Recording: a.rec != nil && a.rec.Busy(), Job: a.jobRunning()}
	if p != nil {
		req.Dirty = p.Dirty()
	}
	if !req.Dirty && !req.Recording && !req.Job {
		return false
	}
	runtime.EventsEmit(a.ctx, "quit-request", req)
	return true
}

// ConfirmQuit completes a quit the UI approved. action is "save" (write staged
// edits first) or "discard"; a running take is stopped and kept, a running
// import or Save As is cancelled.
func (a *App) ConfirmQuit(action string) error {
	if p, err := a.withProject(); err == nil {
		if action == "save" {
			if _, err := a.SaveProject(); err != nil {
				return err
			}
		} else {
			p.Discard()
		}
	}
	a.CancelJob()
	if a.rec.Busy() && a.rec.Status().State != recorder.StateIdle && a.rec.Status().ImportSide == "" {
		if _, err := a.rec.Stop(); err != nil && !errors.Is(err, recorder.ErrEmptyTake) {
			return err
		}
	}
	a.mu.Lock()
	a.quitOK = true
	a.mu.Unlock()
	runtime.Quit(a.ctx)
	return nil
}
