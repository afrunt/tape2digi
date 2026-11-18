package main

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/afrunt/tape2digi/internal/exporter"
	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/settings"
	"github.com/afrunt/tape2digi/internal/wav"
)

// ExportResult is what a finished export reports.
type ExportResult struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"`
}

// exportInput reads the effective audio, markers and details of the project;
// unsaved edits are included and nothing is changed.
func exportInput(p *project.Project) (exporter.Input, error) {
	info := p.Info()
	in := exporter.Input{
		Name: info.Name, Compilation: info.Compilation, Year: info.Year, Album: info.Album, Genre: info.Genre,
	}
	if !info.Compilation {
		in.Artist = info.Artist
	}
	in.TrackArtist = info.Artist
	in.Cover, _ = p.CoverData()
	for _, key := range project.SideKeys {
		if s, ok := info.Sides[key]; ok && !s.Missing {
			if path, err := p.SidePath(key); err == nil {
				if _, err := os.Stat(path); err == nil {
					in.Originals = append(in.Originals, exporter.Original{Name: exporter.OriginalName(key), Path: path})
				}
			}
		}
	}
	for _, key := range project.SideKeys {
		s, ok := info.Sides[key]
		if !ok || s.Missing || len(s.Markers) == 0 {
			continue
		}
		path, err := p.AudioPath(key)
		if err != nil {
			return in, err
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, serr := f.Stat()
		var format wav.Format
		var data uint64
		if serr == nil {
			format, data, serr = wav.Inspect(f, st.Size())
		}
		_ = f.Close()
		if serr != nil {
			return in, serr
		}
		in.Sides = append(in.Sides, exporter.Side{
			Key: key, Path: path, Format: format, Frames: int64(data) / int64(format.BlockAlign()), Markers: s.Markers,
		})
	}
	return in, nil
}

// ExportPlan tells what can be exported: the track count and the formats.
func (a *App) ExportPlan() (exporter.Plan, error) {
	p, err := a.withProject()
	if err != nil {
		return exporter.Plan{}, err
	}
	in, err := exportInput(p)
	if err != nil {
		return exporter.Plan{}, err
	}
	return in.Plan(), nil
}

// ExportPrefs returns the remembered export choices.
func (a *App) ExportPrefs() settings.ExportPrefs {
	if e := a.settings.Load().Export; e != nil {
		return *e
	}
	return settings.DefaultExport()
}

// ExportConflicts lists the files in dir that the export would replace.
func (a *App) ExportConflicts(format, dir string, withOriginals bool) ([]string, error) {
	p, err := a.withProject()
	if err != nil {
		return nil, err
	}
	in, err := exportInput(p)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range in.FilesFor(exporter.Options{Format: format, ExportOriginal: withOriginals}) {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// ExportTracks writes the marked tracks into opts.Dir as a cancellable job.
func (a *App) ExportTracks(opts exporter.Options) (ExportResult, error) {
	p, err := a.withProject()
	if err != nil {
		return ExportResult{}, err
	}
	if a.rec.Busy() {
		return ExportResult{}, i18n.New("app.stopRecordingOrImportingBefore")
	}
	ctx, done, err := a.beginJob()
	if err != nil {
		return ExportResult{}, err
	}
	defer done()
	a.stopPlayback()
	in, err := exportInput(p)
	if err != nil {
		return ExportResult{}, err
	}
	files, err := exporter.Run(ctx, in, opts, func(d, t int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "export", Done: d, Total: t})
		}
	})
	if err != nil {
		return ExportResult{}, err
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	cfg := a.settings.Load()
	prefs := settings.DefaultExport()
	if cfg.Export != nil {
		prefs = *cfg.Export
	}
	prefs.Format, prefs.EmbedCover, prefs.ExportOriginal = opts.Format, opts.EmbedCover, opts.ExportOriginal
	if opts.Format != exporter.FormatFLACCue && opts.CoverMaxMB > 0 {
		prefs.CoverMaxMB = opts.CoverMaxMB
	}
	switch opts.Format {
	case exporter.FormatMP3:
		prefs.Bitrate = opts.Bitrate
	case exporter.FormatWAV:
		prefs.WAVRate = opts.SampleRate
	}
	cfg.Export = &prefs
	_ = a.settings.Save(cfg)
	return ExportResult{Dir: opts.Dir, Files: files}, nil
}

// RevealFolder opens a folder in the OS file manager.
func (a *App) RevealFolder(dir string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return i18n.New("export.badFolder")
	}
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return i18n.New("app.couldNotOpenTheFile", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
