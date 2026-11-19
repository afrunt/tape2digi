package main

import (
	"context"
	"os"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/afrunt/tape2digi/internal/project"
)

// ClipSummary lists the clipped places of a side. Known is false until the
// background scan has analysed the current audio.
type ClipSummary struct {
	Side      string  `json:"side"`
	Known     bool    `json:"known"`
	Count     int     `json:"count"`
	Positions []int64 `json:"positions"`
}

// clipScanner analyses the sides of the open project one at a time, outside
// the job slot, so the side panel can warn about clipping.
type clipScanner struct {
	mu     sync.Mutex
	run    sync.Mutex // one scan at a time
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// stopClips cancels a running scan and waits for it to release its files, so
// a job or recording can replace them.
func (a *App) stopClips() {
	a.clips.mu.Lock()
	if a.clips.cancel != nil {
		a.clips.cancel()
		a.clips.cancel = nil
	}
	a.clips.mu.Unlock()
	a.clips.wg.Wait()
}

// refreshClips restarts the background scan for the open project.
func (a *App) refreshClips() {
	a.mu.Lock()
	p := a.proj
	a.mu.Unlock()
	a.clips.mu.Lock()
	if a.clips.cancel != nil {
		a.clips.cancel()
		a.clips.cancel = nil
	}
	if p == nil || a.ctx == nil {
		a.clips.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.clips.cancel = cancel
	a.clips.wg.Add(1)
	a.clips.mu.Unlock()
	go func() {
		defer a.clips.wg.Done()
		a.clips.run.Lock()
		defer a.clips.run.Unlock()
		for _, side := range project.SideKeys {
			if ctx.Err() != nil {
				return
			}
			path, err := p.AudioPath(side)
			if err != nil {
				continue
			}
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if _, err := a.peaks(ctx, p, side); err != nil {
				continue
			}
			if ctx.Err() == nil {
				runtime.EventsEmit(a.ctx, "clips", a.clipSummary(p, side))
			}
		}
	}()
}

// clipSummary reads the cached analysis without starting one.
func (a *App) clipSummary(p *project.Project, side string) ClipSummary {
	s := ClipSummary{Side: side, Positions: []int64{}}
	path, err := p.AudioPath(side)
	if err != nil {
		return s
	}
	st, err := os.Stat(path)
	if err != nil {
		return s
	}
	a.peakMu.Lock()
	e, ok := a.peakCache[side]
	a.peakMu.Unlock()
	if !ok || e.path != path || e.size != st.Size() || !e.mtime.Equal(st.ModTime()) {
		return s
	}
	rate := int64(e.peaks.Format.SampleRate)
	s.Known, s.Count = true, e.peaks.ClipCount
	for _, f := range e.peaks.Clips {
		s.Positions = append(s.Positions, f*1000/rate)
	}
	return s
}

// SideClips returns the side's clipped places in milliseconds once known.
func (a *App) SideClips(side string) (ClipSummary, error) {
	p, err := a.withProject()
	if err != nil {
		return ClipSummary{Side: side, Positions: []int64{}}, err
	}
	return a.clipSummary(p, side), nil
}
