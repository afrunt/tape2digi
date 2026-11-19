package main

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/lookup"
	"github.com/afrunt/tape2digi/internal/project"
)

var errLookupCancelled = i18n.New("lookup.cancelled")

// LookupPreview is a release mapped onto the recorded sides.
type LookupPreview struct {
	Release lookup.Detail  `json:"release"`
	Media   []int          `json:"media"`
	Mapping lookup.Mapping `json:"mapping"`
	// Recorded is the number of tracks already marked on each side.
	Recorded map[string]int `json:"recorded"`
}

// LookupResult reports what Apply changed.
type LookupResult struct {
	Info project.Info `json:"info"`
	// Unmatched counts listing tracks per side that had no recorded track.
	Unmatched map[string]int `json:"unmatched"`
	// CoverError explains why the cover was not used; empty when it was.
	CoverError string `json:"coverError"`
}

// SetOnlineLookup allows or forbids sending search text to MusicBrainz.
func (a *App) SetOnlineLookup(on bool) error {
	a.settingsMu.Lock()
	cfg := a.settings.Load()
	cfg.OnlineLookup = on
	err := a.settings.Save(cfg)
	a.settingsMu.Unlock()
	a.menuMu.Lock()
	if a.lookupItem != nil {
		a.lookupItem.Checked = on
	}
	a.menuMu.Unlock()
	if a.ctx != nil {
		runtime.MenuUpdateApplicationMenu(a.ctx)
		runtime.EventsEmit(a.ctx, "settings", a.settings.Load())
	}
	return err
}

// SetTracksReady tells the backend whether the open project has a side with a
// complete track, which enables File > Look up tracks.
func (a *App) SetTracksReady(ready bool) {
	a.menuMu.Lock()
	changed := a.tracksOK != ready
	a.tracksOK = ready
	a.menuMu.Unlock()
	if changed {
		a.updateMenu()
	}
}

func (a *App) buildToolsMenu() *menu.Menu {
	m := menu.NewMenu()
	a.menuMu.Lock()
	a.lookupItem = m.AddCheckbox(i18n.T("app.menu.onlineLookup"), a.settings.Load().OnlineLookup, nil, func(cd *menu.CallbackData) {
		if err := a.SetOnlineLookup(cd.MenuItem.Checked); err != nil {
			runtime.EventsEmit(a.ctx, "error", err.Error())
		}
	})
	a.menuMu.Unlock()
	return m
}

// lookupJob runs fn as the cancellable job; it refuses while lookup is off.
func (a *App) lookupJob(fn func(ctx context.Context, p *project.Project) error) error {
	if !a.settings.Load().OnlineLookup {
		return i18n.New("lookup.disabled")
	}
	ctx, done, err := a.beginJob()
	if err != nil {
		return err
	}
	defer done()
	a.mu.Lock()
	p := a.proj
	a.mu.Unlock()
	if err := fn(ctx, p); err != nil {
		if errors.Is(err, context.Canceled) {
			return errLookupCancelled
		}
		return err
	}
	return nil
}

// LookupSearch finds releases by artist and album text.
func (a *App) LookupSearch(artist, album string) ([]lookup.Release, error) {
	var out []lookup.Release
	err := a.lookupJob(func(ctx context.Context, _ *project.Project) error {
		var err error
		out, err = a.lookup.Search(ctx, artist, album)
		return err
	})
	return out, err
}

func recordedTracks(info project.Info) map[string]int {
	out := map[string]int{}
	for k, s := range info.Sides {
		for _, m := range s.Markers {
			if m.Kind == project.MarkerStart {
				out[k]++
			}
		}
	}
	return out
}

func (a *App) preview(info project.Info, d lookup.Detail, media []int) LookupPreview {
	if media == nil {
		media = lookup.DefaultMedia(d)
	}
	m := lookup.Map(d, media, info.Sides["A"].DurationMs, info.Sides["B"].DurationMs)
	if _, hasA := info.Sides["A"]; !hasA && len(m.B) == 0 {
		m = lookup.Mapping{B: m.A}
	}
	return LookupPreview{Release: d, Media: media, Mapping: m, Recorded: recordedTracks(info)}
}

// LookupRelease fetches a release and maps the chosen media (nil: the
// default) onto the sides. Remapping the release shown last needs no request.
func (a *App) LookupRelease(mbid string, media []int) (LookupPreview, error) {
	a.lookupMu.Lock()
	last := a.lookupLast
	a.lookupMu.Unlock()
	if last.MBID == mbid && mbid != "" {
		p, err := a.withProject()
		if err != nil {
			return LookupPreview{}, err
		}
		return a.preview(p.Info(), last, media), nil
	}
	var out LookupPreview
	err := a.lookupJob(func(ctx context.Context, p *project.Project) error {
		d, err := a.lookup.Release(ctx, mbid)
		if err != nil {
			return err
		}
		a.lookupMu.Lock()
		a.lookupLast = d
		a.lookupMu.Unlock()
		out = a.preview(p.Info(), d, media)
		return nil
	})
	return out, err
}

// LookupApply fills track titles, empty project details and optionally the
// cover from the release shown by LookupRelease, and stores each side's
// listing for Split by tracklist. Each side's titles are one undoable step.
func (a *App) LookupApply(mbid string, media []int, useCover bool) (LookupResult, error) {
	res := LookupResult{Unmatched: map[string]int{}}
	a.lookupMu.Lock()
	d := a.lookupLast
	a.lookupMu.Unlock()
	if d.MBID == "" || d.MBID != mbid {
		return res, i18n.New("lookup.pickRelease")
	}
	if err := a.editorBusy(); err != nil {
		return res, err
	}
	var cover []byte
	if useCover {
		err := a.lookupJob(func(ctx context.Context, _ *project.Project) error {
			var err error
			cover, _, err = a.lookup.Cover(ctx, mbid)
			return err
		})
		if err != nil {
			if errors.Is(err, errLookupCancelled) {
				return res, err
			}
			res.CoverError = err.Error()
		}
	} else if !a.settings.Load().OnlineLookup {
		return res, i18n.New("lookup.disabled")
	}
	p, err := a.withProject()
	if err != nil {
		return res, err
	}
	info := p.Info()
	pv := a.preview(info, d, media)
	for side, tracks := range map[string][]lookup.Track{"A": pv.Mapping.A, "B": pv.Mapping.B} {
		if _, ok := info.Sides[side]; !ok || len(tracks) == 0 {
			continue
		}
		listing := lookup.Listing(tracks, d.Artist)
		if err := p.SetListing(side, listing); err != nil {
			return res, err
		}
		cur := p.Markers(side)
		next, unmatched := lookup.TitleMarkers(cur, listing)
		res.Unmatched[side] = unmatched
		if !sameMarkers(cur, next) {
			if err := p.ReplaceMarkers(side, next); err != nil {
				return res, err
			}
		}
	}
	info = p.Info()
	det := lookup.FillDetails(project.Details{Artist: info.Artist, Compilation: info.Compilation, Year: info.Year, Album: info.Album, Genre: info.Genre}, d.Release)
	if err := p.SetDetails(det.Artist, det.Compilation, det.Year, det.Album, det.Genre); err != nil {
		return res, err
	}
	if cover != nil {
		if err := p.SetCoverData(cover); err != nil {
			res.CoverError = err.Error()
		}
	}
	res.Info = p.Info()
	return res, nil
}

func sameMarkers(a, b []project.Marker) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
