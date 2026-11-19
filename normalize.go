package main

import (
	"context"
	"math"
	"os"
	"time"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/loudness"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/wav"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type loudEntry struct {
	path  string
	size  int64
	mtime time.Time
	res   loudness.Result
}

// SideLoudness is what the Normalize dialog shows.
type SideLoudness struct {
	Available bool    `json:"available"` // false for silence
	LUFS      float64 `json:"lufs"`
	PeakDB    float64 `json:"peakDb"`
	Target    float64 `json:"target"`
	GainDB    float64 `json:"gainDb"`
	Capped    bool    `json:"capped"`
	// Applicable is false when the change would be too small to matter.
	Applicable bool    `json:"applicable"`
	MinTarget  float64 `json:"minTarget"`
	MaxTarget  float64 `json:"maxTarget"`
}

func loudnessFor(r loudness.Result, target float64) SideLoudness {
	s := SideLoudness{Target: target, MinTarget: loudness.MinTarget, MaxTarget: loudness.MaxTarget}
	l, ok := r.LUFS()
	if !ok || r.Peak <= 0 {
		return s
	}
	s.Available, s.LUFS, s.PeakDB = true, l, 20*math.Log10(r.Peak)
	s.GainDB, s.Capped = loudness.NormalizeGain(l, r.Peak, target)
	s.Applicable = math.Abs(s.GainDB) >= loudness.MinGain
	return s
}

// measureSide returns the side's loudness, cached until its audio changes.
func (a *App) measureSide(ctx context.Context, p *project.Project, side string) (loudness.Result, error) {
	path, err := p.AudioPath(side)
	if err != nil {
		return loudness.Result{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return loudness.Result{}, i18n.New("project.sideHasNoAudio", side)
	}
	a.peakMu.Lock()
	e, ok := a.loudCache[side]
	a.peakMu.Unlock()
	if ok && e.path == path && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.res, nil
	}
	total := st.Size()
	res, _, err := loudness.MeasureFile(ctx, path, 0, -1, func(n int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "loudness", Side: side, Done: n, Total: total})
		}
	})
	if err != nil {
		return loudness.Result{}, err
	}
	a.peakMu.Lock()
	if a.loudCache == nil {
		a.loudCache = map[string]loudEntry{}
	}
	a.loudCache[side] = loudEntry{path: path, size: st.Size(), mtime: st.ModTime(), res: res}
	a.peakMu.Unlock()
	return res, nil
}

func clampTarget(t float64) float64 {
	if math.IsNaN(t) {
		return loudness.DefaultTarget
	}
	return min(max(t, loudness.MinTarget), loudness.MaxTarget)
}

// SideLoudnessInfo measures the side (cancellable, with progress) and
// reports the gain that would bring it to target LUFS.
func (a *App) SideLoudnessInfo(side string, target float64) (SideLoudness, error) {
	p, err := a.withProject()
	if err != nil {
		return SideLoudness{}, err
	}
	ctx, done, err := a.beginJob()
	if err != nil {
		return SideLoudness{}, err
	}
	defer done()
	r, err := a.measureSide(ctx, p, side)
	if err != nil {
		return SideLoudness{}, err
	}
	return loudnessFor(r, clampTarget(target)), nil
}

// NormalizeSide sets the side's integrated loudness to target LUFS without
// letting the peak pass -1 dBFS, as one staged, undoable edit.
func (a *App) NormalizeSide(side string, target float64) (project.Info, error) {
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
	defer done()
	a.stopPlayback()
	src, dst, err := p.EditPaths(side)
	if err != nil {
		return p.Info(), err
	}
	r, err := a.measureSide(ctx, p, side)
	if err != nil {
		return p.Info(), err
	}
	info := loudnessFor(r, clampTarget(target))
	if !info.Applicable {
		return p.Info(), i18n.New("app.normalizeUnavailable")
	}
	tmp := dst + ".norm"
	if err := wav.Amplify(ctx, src, tmp, wav.GainFromDB(info.GainDB), func(d, t int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "normalize", Side: side, Done: d, Total: t})
		}
	}); err != nil {
		return p.Info(), err
	}
	if err := p.ApplyEdit(side, tmp); err != nil {
		_ = os.Remove(tmp)
		return p.Info(), err
	}
	a.dropPeaks(side)
	return p.Info(), nil
}
