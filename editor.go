package main

import (
	"context"
	"os"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/denoise"
	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/split"
	"github.com/afrunt/tape2digi/internal/wav"
)

// peakEntry is a cached peak table; it is valid while the file is unchanged.
type peakEntry struct {
	path  string
	size  int64
	mtime time.Time
	peaks *wav.Peaks
}

type splitEntry struct {
	path    string
	size    int64
	mtime   time.Time
	markers []project.Marker
}

// envEntry is a cached level envelope; it is valid while the file is unchanged.
type envEntry struct {
	path  string
	size  int64
	mtime time.Time
	env   []float32
}

// NoisePlan tells whether a steady noise pattern was found in the side.
type NoisePlan struct {
	Available bool    `json:"available"`
	NoiseDb   float64 `json:"noiseDb"`
}

// SplitPlan tells whether the side can be split into tracks automatically.
type SplitPlan struct {
	Available bool `json:"available"`
	Tracks    int  `json:"tracks"`
}

// Waveform is the per-channel peak table of a side, resampled for drawing.
type Waveform struct {
	Channels      int         `json:"channels"`
	SampleRate    int         `json:"sampleRate"`
	DurationMs    int64       `json:"durationMs"`
	Min           [][]float32 `json:"min"`
	Max           [][]float32 `json:"max"`
	GainAvailable bool        `json:"gainAvailable"`
	GainDB        float64     `json:"gainDb"`
}

// SideGain reports the maximum lossless amplification of a side.
type SideGain struct {
	Available bool    `json:"available"`
	DB        float64 `json:"db"`
}

// SidePlayback is the state of the side editor's playback.
type SidePlayback struct {
	Side       string `json:"side"`
	Active     bool   `json:"active"`
	Playing    bool   `json:"playing"`
	PositionMs int64  `json:"positionMs"`
	DurationMs int64  `json:"durationMs"`
	// Levels is the loudest level per channel since the previous poll.
	Levels []audio.ChannelLevel `json:"levels"`
}

// MarkerSpan is the range a marker may be dragged within.
type MarkerSpan struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// MarkerChoice tells which markers the context menu may offer at a position.
type MarkerChoice struct {
	Start bool `json:"start"`
	End   bool `json:"end"`
}

func (a *App) playerOrNew() *audio.Player {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.player == nil {
		a.player = audio.NewPlayer(a.src)
	}
	return a.player
}

func (a *App) stopPlayback() {
	a.mu.Lock()
	pl := a.player
	a.playSide = ""
	a.mu.Unlock()
	if pl != nil {
		pl.Stop()
	}
}

// editorBusy refuses editing while a recording, import or job runs.
func (a *App) editorBusy() error {
	if a.jobRunning() {
		return i18n.New("app.waitForTheCurrentOperation")
	}
	if a.rec != nil && a.rec.Busy() {
		return i18n.New("app.stopRecordingOrImportingBefore")
	}
	return nil
}

func (a *App) jobContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// peaks returns the side's peak table, computing it when the file changed.
func (a *App) peaks(ctx context.Context, p *project.Project, side string) (*wav.Peaks, error) {
	path, err := p.AudioPath(side)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, i18n.New("project.sideHasNoAudio", side)
	}
	a.peakMu.Lock()
	e, ok := a.peakCache[side]
	a.peakMu.Unlock()
	if ok && e.path == path && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.peaks, nil
	}
	pk, err := wav.Analyze(ctx, path)
	if err != nil {
		return nil, err
	}
	a.peakMu.Lock()
	if a.peakCache == nil {
		a.peakCache = map[string]peakEntry{}
	}
	a.peakCache[side] = peakEntry{path: path, size: st.Size(), mtime: st.ModTime(), peaks: pk}
	a.peakMu.Unlock()
	return pk, nil
}

func gainOf(pk *wav.Peaks) (wav.Gain, bool) {
	return wav.MaxGain(pk.MaxPos, pk.MaxNeg, pk.Format.BitDepth)
}

// SideWaveform returns the side's peaks resampled to at most buckets columns.
func (a *App) SideWaveform(side string, buckets int) (Waveform, error) {
	p, err := a.withProject()
	if err != nil {
		return Waveform{}, err
	}
	pk, err := a.peaks(a.jobContext(), p, side)
	if err != nil {
		return Waveform{}, err
	}
	if buckets <= 0 || buckets > pk.Buckets {
		buckets = pk.Buckets
	}
	w := Waveform{Channels: len(pk.Min), SampleRate: pk.Format.SampleRate}
	if pk.Format.SampleRate > 0 {
		w.DurationMs = pk.Frames * 1000 / int64(pk.Format.SampleRate)
	}
	for ch := range pk.Min {
		mins, maxs := make([]float32, buckets), make([]float32, buckets)
		for i := range mins {
			from, to := i*pk.Buckets/buckets, (i+1)*pk.Buckets/buckets
			if to <= from {
				to = from + 1
			}
			lo, hi := pk.Min[ch][from], pk.Max[ch][from]
			for j := from + 1; j < to; j++ {
				lo, hi = min(lo, pk.Min[ch][j]), max(hi, pk.Max[ch][j])
			}
			mins[i], maxs[i] = lo, hi
		}
		w.Min, w.Max = append(w.Min, mins), append(w.Max, maxs)
	}
	if g, ok := gainOf(pk); ok {
		w.GainAvailable, w.GainDB = true, g.DB()
	}
	return w, nil
}

// maxRangeBuckets caps the columns of one ranged waveform request.
const maxRangeBuckets = 8192

// SideWaveformRange returns the peaks of the window [fromMs, toMs) of the
// side, read from the audio file so zoomed views keep their detail.
func (a *App) SideWaveformRange(side string, fromMs, toMs int64, buckets int) (Waveform, error) {
	p, err := a.withProject()
	if err != nil {
		return Waveform{}, err
	}
	pk, err := a.peaks(a.jobContext(), p, side)
	if err != nil {
		return Waveform{}, err
	}
	rate := int64(pk.Format.SampleRate)
	from, to := max(fromMs, 0)*rate/1000, toMs*rate/1000
	if to <= from || from >= pk.Frames || rate <= 0 {
		return Waveform{}, i18n.New("app.emptyWaveformRange")
	}
	buckets = min(max(buckets, 1), maxRangeBuckets)
	path, err := p.AudioPath(side)
	if err != nil {
		return Waveform{}, err
	}
	mn, mx, err := wav.RangePeaks(a.jobContext(), path, from, to, buckets)
	if err != nil {
		return Waveform{}, err
	}
	w := Waveform{Channels: len(mn), SampleRate: pk.Format.SampleRate, DurationMs: pk.Frames * 1000 / rate, Min: mn, Max: mx}
	if g, ok := gainOf(pk); ok {
		w.GainAvailable, w.GainDB = true, g.DB()
	}
	return w, nil
}

// SideGainInfo reports whether Amplify would change the side and by how much.
func (a *App) SideGainInfo(side string) (SideGain, error) {
	p, err := a.withProject()
	if err != nil {
		return SideGain{}, err
	}
	pk, err := a.peaks(a.jobContext(), p, side)
	if err != nil {
		return SideGain{}, err
	}
	g, ok := gainOf(pk)
	if !ok {
		return SideGain{}, nil
	}
	return SideGain{Available: true, DB: g.DB()}, nil
}

// AmplifySide raises the side to the highest level without clipping. The
// result is a staged copy; the saved file changes only on Save.
func (a *App) AmplifySide(side string) (project.Info, error) {
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
	pk, err := a.peaks(ctx, p, side)
	if err != nil {
		return p.Info(), err
	}
	g, ok := gainOf(pk)
	if !ok {
		return p.Info(), i18n.New("app.amplifyUnavailable")
	}
	tmp := dst + ".amp"
	if err := wav.Amplify(ctx, src, tmp, g, func(d, t int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "amplify", Side: side, Done: d, Total: t})
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

func (a *App) dropPeaks(side string) {
	a.peakMu.Lock()
	delete(a.peakCache, side)
	delete(a.splitCache, side)
	delete(a.envCache, side)
	a.peakMu.Unlock()
}

// EditHistory reports whether the side has a change to undo or redo.
func (a *App) EditHistory(side string) (project.History, error) {
	p, err := a.withProject()
	if err != nil {
		return project.History{}, err
	}
	return p.History(side), nil
}

// UndoSide reverts the side's last editor change.
func (a *App) UndoSide(side string) (project.Info, error) { return a.stepHistory(side, true) }

// RedoSide applies the editor change that was last undone.
func (a *App) RedoSide(side string) (project.Info, error) { return a.stepHistory(side, false) }

// EditHistoryLast reports what an undo or redo from the main window would act on.
func (a *App) EditHistoryLast() (project.LastHistory, error) {
	p, err := a.withProject()
	if err != nil {
		return project.LastHistory{}, err
	}
	return p.LastHistory(), nil
}

// UndoLast reverts the most recent editor change on any side.
func (a *App) UndoLast() (project.Info, error) { return a.stepLast(true) }

// RedoLast reapplies the most recently undone editor change on any side.
func (a *App) RedoLast() (project.Info, error) { return a.stepLast(false) }

func (a *App) stepLast(undo bool) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	last := p.LastHistory()
	side := last.RedoSide
	if undo {
		side = last.UndoSide
	}
	if side == "" {
		return p.Info(), nil
	}
	return a.stepHistory(side, undo)
}

func (a *App) stepHistory(side string, undo bool) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	a.stopPlayback()
	var audioChanged bool
	if undo {
		audioChanged, err = p.Undo(side)
	} else {
		audioChanged, err = p.Redo(side)
	}
	if err != nil {
		return p.Info(), err
	}
	if audioChanged {
		a.dropPeaks(side)
	}
	return p.Info(), nil
}

// PlaySide plays the side from ms on the chosen monitor output.
func (a *App) PlaySide(side string, ms int64) error {
	p, err := a.withProject()
	if err != nil {
		return err
	}
	if err := a.editorBusy(); err != nil {
		return err
	}
	path, err := p.AudioPath(side)
	if err != nil {
		return err
	}
	if a.rec.Status().Monitor {
		if err := a.rec.SetMonitor(false); err != nil {
			return err
		}
	}
	if err := a.playerOrNew().Start(path, a.rec.OutputID(), ms); err != nil {
		return err
	}
	a.mu.Lock()
	a.playSide = side
	a.mu.Unlock()
	return nil
}

func (a *App) PauseSidePlayback()  { a.playerOrNew().Pause() }
func (a *App) ResumeSidePlayback() { a.playerOrNew().Resume() }

// SeekSide moves the playhead; playback continues if it was running.
func (a *App) SeekSide(ms int64) { a.playerOrNew().SeekTo(ms) }

// StopSidePlayback stops playback and releases the file.
func (a *App) StopSidePlayback() { a.stopPlayback() }

// SidePlaybackState returns the playback state polled by the editor.
func (a *App) SidePlaybackState() SidePlayback {
	st := a.playerOrNew().State()
	a.mu.Lock()
	side := a.playSide
	a.mu.Unlock()
	return SidePlayback{Side: side, Active: st.Active, Playing: st.Playing, PositionMs: st.PositionMs, DurationMs: st.DurationMs, Levels: st.Levels}
}

// MarkerOptions tells which markers can be inserted at ms.
func (a *App) MarkerOptions(side string, ms int64) (MarkerChoice, error) {
	p, err := a.withProject()
	if err != nil {
		return MarkerChoice{}, err
	}
	s, e := p.MarkerOptions(side, ms)
	return MarkerChoice{Start: s, End: e}, nil
}

// AddMarker stages a track start ("start") or end ("end") marker.
func (a *App) AddMarker(side, kind string, ms int64) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	err = p.AddMarker(side, kind, ms)
	return p.Info(), err
}

// SetTrackDetails stages the title, artist and album artist of the track that
// starts at startMs.
func (a *App) SetTrackDetails(side string, startMs int64, title, artist, albumArtist string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	err = p.SetTrackDetails(side, startMs, title, artist, albumArtist)
	return p.Info(), err
}

// MarkerRange returns the lowest and highest position the marker at ms may be
// dragged to without passing a neighbour.
func (a *App) MarkerRange(side string, ms int64) (MarkerSpan, error) {
	p, err := a.withProject()
	if err != nil {
		return MarkerSpan{}, err
	}
	lo, hi, err := p.MarkerRange(side, ms)
	return MarkerSpan{Min: lo, Max: hi}, err
}

// MoveMarker stages a new position for the marker at fromMs.
func (a *App) MoveMarker(side string, fromMs, toMs int64) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	err = p.MoveMarker(side, fromMs, toMs)
	return p.Info(), err
}

// RemoveMarker stages the removal of the marker at ms; a start also removes
// the end that closes it.
func (a *App) RemoveMarker(side string, ms int64) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	err = p.RemoveMarker(side, ms)
	return p.Info(), err
}

// splitMarkers returns the detected track markers of the side, cached per file version.
func (a *App) splitMarkers(ctx context.Context, p *project.Project, side string) ([]project.Marker, error) {
	path, err := p.AudioPath(side)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, i18n.New("project.sideHasNoAudio", side)
	}
	a.peakMu.Lock()
	e, ok := a.splitCache[side]
	a.peakMu.Unlock()
	if ok && e.path == path && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.markers, nil
	}
	env, err := a.envelope(ctx, p, side)
	if err != nil {
		return nil, err
	}
	markers := split.Detect(env)
	a.peakMu.Lock()
	if a.splitCache == nil {
		a.splitCache = map[string]splitEntry{}
	}
	a.splitCache[side] = splitEntry{path: path, size: st.Size(), mtime: st.ModTime(), markers: markers}
	a.peakMu.Unlock()
	return markers, nil
}

// envelope returns the side's level envelope, cached per file version.
func (a *App) envelope(ctx context.Context, p *project.Project, side string) ([]float32, error) {
	path, err := p.AudioPath(side)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, i18n.New("project.sideHasNoAudio", side)
	}
	a.peakMu.Lock()
	e, ok := a.envCache[side]
	a.peakMu.Unlock()
	if ok && e.path == path && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.env, nil
	}
	env, err := wav.Envelope(ctx, path, split.WindowMs)
	if err != nil {
		return nil, err
	}
	a.peakMu.Lock()
	if a.envCache == nil {
		a.envCache = map[string]envEntry{}
	}
	a.envCache[side] = envEntry{path: path, size: st.Size(), mtime: st.ModTime(), env: env}
	a.peakMu.Unlock()
	return env, nil
}

// SideNoisePlan reports whether Fix Noise is possible; it never changes the project.
func (a *App) SideNoisePlan(side string) (NoisePlan, error) {
	p, err := a.withProject()
	if err != nil {
		return NoisePlan{}, err
	}
	env, err := a.envelope(a.jobContext(), p, side)
	if err != nil {
		return NoisePlan{}, err
	}
	plan, ok := denoise.Analyze(env)
	return NoisePlan{Available: ok, NoiseDb: plan.NoiseDB}, nil
}

// DenoiseSide removes steady noise from the side as one undoable edit.
func (a *App) DenoiseSide(side string) (project.Info, error) {
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
	env, err := a.envelope(ctx, p, side)
	if err != nil {
		return p.Info(), err
	}
	plan, ok := denoise.Analyze(env)
	if !ok {
		return p.Info(), i18n.New("app.noiseUnavailable")
	}
	tmp := dst + ".den"
	if err := denoise.Process(ctx, src, tmp, plan, func(d, t int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "import", ImportProgress{Kind: "denoise", Side: side, Done: d, Total: t})
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

// SideSplitPlan reports whether Split is possible; it never changes the project.
func (a *App) SideSplitPlan(side string) (SplitPlan, error) {
	p, err := a.withProject()
	if err != nil {
		return SplitPlan{}, err
	}
	markers, err := a.splitMarkers(a.jobContext(), p, side)
	if err != nil {
		return SplitPlan{}, err
	}
	return SplitPlan{Available: len(markers) > 0, Tracks: len(markers) / 2}, nil
}

// SplitSide replaces the side's markers with the detected tracks as one undoable step.
func (a *App) SplitSide(side string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	markers, err := a.splitMarkers(a.jobContext(), p, side)
	if err != nil {
		return p.Info(), err
	}
	if len(markers) == 0 {
		return p.Info(), i18n.New("app.splitUnavailable")
	}
	err = p.ReplaceMarkers(side, markers)
	return p.Info(), err
}

// ResetMarkers stages an empty marker set for a side as one undoable step.
func (a *App) ResetMarkers(side string) (project.Info, error) {
	p, err := a.withProject()
	if err != nil {
		return project.Info{}, err
	}
	if err := a.editorBusy(); err != nil {
		return p.Info(), err
	}
	if len(p.Markers(side)) == 0 {
		return p.Info(), nil
	}
	err = p.ReplaceMarkers(side, nil)
	return p.Info(), err
}
