package main

import (
	"context"
	"math"
	"os"
	"time"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/repair"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type repairEntry struct {
	path  string
	size  int64
	mtime time.Time
	a     repair.Analysis
}

func (a *App) emitProgress(kind, side string) func(d, t int64) {
	return func(d, t int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: kind, Side: side, Done: d, Total: t})
		}
	}
}

// repairAnalysis analyses the side, cached until its audio changes.
func (a *App) repairAnalysis(ctx context.Context, p *project.Project, side string) (repair.Analysis, error) {
	path, err := p.AudioPath(side)
	if err != nil {
		return repair.Analysis{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return repair.Analysis{}, i18n.New("project.sideHasNoAudio", side)
	}
	a.peakMu.Lock()
	e, ok := a.repairCache[side]
	a.peakMu.Unlock()
	if ok && e.path == path && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.a, nil
	}
	env, err := a.envelope(ctx, p, side)
	if err != nil {
		return repair.Analysis{}, err
	}
	res, err := repair.Analyze(ctx, path, env, a.emitProgress("repairScan", side))
	if err != nil {
		return repair.Analysis{}, err
	}
	a.peakMu.Lock()
	if a.repairCache == nil {
		a.repairCache = map[string]repairEntry{}
	}
	a.repairCache[side] = repairEntry{path: path, size: st.Size(), mtime: st.ModTime(), a: res}
	a.peakMu.Unlock()
	return res, nil
}

// SideRepairPlan analyses the side for tape faults (cancellable, with
// progress); it never changes the project.
func (a *App) SideRepairPlan(side string) (repair.Analysis, error) {
	p, err := a.withProject()
	if err != nil {
		return repair.Analysis{}, err
	}
	ctx, done, err := a.beginJob()
	if err != nil {
		return repair.Analysis{}, err
	}
	defer done()
	return a.repairAnalysis(ctx, p, side)
}

// RepairSide applies one repair tool as an undoable staged edit. mode is the
// Mono choice (left, right or mix); balanceDB is the right-minus-left
// difference to correct (±6 dB).
func (a *App) RepairSide(side, tool, mode string, balanceDB float64) (project.Info, error) {
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
	an, err := a.repairAnalysis(ctx, p, side)
	if err != nil {
		return p.Info(), err
	}
	op := an.Op(tool)
	stereo := an.Channels == 2
	ok := false
	switch tool {
	case repair.ToolDC:
		ok = an.DCFix
	case repair.ToolHum:
		ok = an.HumHz != 0
	case repair.ToolAzimuth:
		ok = stereo && an.DelayFix
	case repair.ToolBalance:
		op.BalanceDB = balanceDB
		ok = stereo && !math.IsNaN(balanceDB) && math.Abs(balanceDB) <= repair.MaxBalanceDB && math.Abs(balanceDB) >= 0.05
	case repair.ToolMono:
		op.Mono = mode
		ok = stereo && (mode == repair.MonoLeft || mode == repair.MonoRight || mode == repair.MonoMix)
	}
	if !ok {
		return p.Info(), i18n.New("app.repairUnavailable")
	}
	tmp := dst + ".rep"
	if err := repair.Process(ctx, src, tmp, op, a.emitProgress("repair", side)); err != nil {
		return p.Info(), err
	}
	if err := p.ApplyEdit(side, tmp); err != nil {
		_ = os.Remove(tmp)
		return p.Info(), err
	}
	a.dropPeaks(side)
	return p.Info(), nil
}
