// Package split proposes track boundaries for a recording from its level
// envelope: tracks are separated by stretches that are much quieter than the
// music.
package split

import (
	"sort"

	"github.com/afrunt/tape2digi/internal/project"
)

const (
	// WindowMs is the envelope resolution Detect expects.
	WindowMs = 50

	minSpreadDB  = 20.0 // music must be this much louder than the noise floor
	thresholdPos = 0.4  // silence threshold: this share of the way from floor to music
	minGapMs     = 2000
	minTrackMs   = 10000
	padMs        = 250 // markers sit this far into the silence around a track

	// Tape pauses are often a fade-out ending in a brief dip to the hiss: a
	// gap is also a fadeMs-long run below fadePos that holds a deepMs-long
	// run below deepPos (shares of the way from floor to music).
	fadePos = 0.6
	deepPos = 0.25
	fadeMs  = 2000
	deepMs  = 500

	digitalSilenceDB = -90 // below this a window is digital silence, not tape hiss
	floorPermille    = 10  // the floor is this low share of the hiss and music windows

	// Songs may also be separated without a pause to the hiss: a fade dipping
	// well below the music on both sides, or a sharp drop into a quieter next
	// song. Levels are compared with the music around each point.
	smoothMs      = 1000  // the level of a point is the mean over this span around it
	contextNearMs = 10000 // the music around a point starts this far from it
	contextFarMs  = 30000 // and ends this far from it
	contextMinMs  = 10000 // shorter music context gives no boundary
	contextStep   = 5     // windows that share one context evaluation (250 ms)
	dipDB         = 6.0   // a dip is at least this far below the music on both sides
	dipDeepDB     = 10.0  // and somewhere at least this far
	dipMs         = 6000  // for at least this long
	dropBeforeDB  = 15.0  // a drop is at least this far below the music before
	dropAfterDB   = 4.0   // and at least this far below the music after
	dropAfterMax  = 10.0  // but less than this (the next song starts quieter)
	dropMs        = 2500  // for at least this long
)

// Span is a stretch of envelope windows, To exclusive.
type Span struct{ From, To int }

// Detect returns alternating start and end markers for the tracks found in
// env (dBFS per WindowMs window), or nil when no confident split of at least
// two tracks exists.
//
// Tracks separated by a pause get markers padMs into the pause. Tracks
// separated by a fade or a drop, which are music, share one cut at its
// quietest point: the next track starts one window after the end.
func Detect(env []float32) []project.Marker {
	kept := pieces(env)
	cuts := contrastCuts(env, kept)
	count := len(kept)
	for _, c := range cuts {
		count += len(c)
	}
	if count < 2 {
		return nil
	}

	total := int64(len(env)) * WindowMs
	out := make([]project.Marker, 0, 2*count)
	for i, t := range kept {
		s := int64(t.From)*WindowMs - padMs
		if s < 0 {
			s = 0
		}
		for _, c := range cuts[i] {
			e := int64(c) * WindowMs
			out = append(out, project.Marker{Kind: project.MarkerStart, PositionMs: s}, project.Marker{Kind: project.MarkerEnd, PositionMs: e})
			s = e + WindowMs
		}
		e := int64(t.To)*WindowMs + padMs
		if e > total {
			e = total
		}
		out = append(out, project.Marker{Kind: project.MarkerStart, PositionMs: s}, project.Marker{Kind: project.MarkerEnd, PositionMs: e})
	}
	return out
}

// contrastCuts returns, for each piece, the windows where it holds a fade or
// a drop into the next song, keeping at least minTrackMs between cuts and
// piece edges.
func contrastCuts(env []float32, kept []Span) [][]int {
	cuts := make([][]int, len(kept))
	if len(kept) == 0 {
		return cuts
	}
	n := len(env)
	sm := smooth(env, smoothMs/WindowMs)
	dip := make([]bool, n)
	deep := make([]bool, n)
	drop := make([]bool, n)
	near, far, least := contextNearMs/WindowMs, contextFarMs/WindowMs, contextMinMs/WindowMs
	for i := 0; i < n; i += contextStep {
		before, okB := musicAround(env, i-far, i-near, least)
		after, okA := musicAround(env, i+near, i+far, least)
		if !okB || !okA {
			continue
		}
		low := min(before, after)
		for k := i; k < min(i+contextStep, n); k++ {
			dip[k] = sm[k] <= low-dipDB
			deep[k] = sm[k] <= low-dipDeepDB
			drop[k] = sm[k] <= before-dropBeforeDB && sm[k] <= after-dropAfterDB && sm[k] > after-dropAfterMax
		}
	}

	var found []Span
	for _, r := range flagged(dip) {
		if (r.To-r.From)*WindowMs < dipMs {
			continue
		}
		for k := r.From; k < r.To; k++ {
			if deep[k] {
				found = append(found, r)
				break
			}
		}
	}
	for _, r := range flagged(drop) {
		if (r.To-r.From)*WindowMs >= dropMs {
			found = append(found, r)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].From < found[j].From })

	margin := minTrackMs / WindowMs
	for _, r := range found {
		for i, p := range kept {
			if r.From-p.From < margin || p.To-r.To < margin {
				continue
			}
			c := r.From
			for k := r.From; k < r.To; k++ {
				if sm[k] < sm[c] {
					c = k
				}
			}
			last := p.From
			if l := len(cuts[i]); l > 0 {
				last = cuts[i][l-1]
			}
			if c-last >= margin && p.To-c >= margin {
				cuts[i] = append(cuts[i], c)
			}
		}
	}
	return cuts
}

// smooth returns the mean of env over w windows centred on each window.
func smooth(env []float32, w int) []float32 {
	out := make([]float32, len(env))
	for i := range env {
		a, b := max(i-w/2, 0), min(i+w/2, len(env))
		var s float32
		for _, v := range env[a:b] {
			s += v
		}
		out[i] = s / float32(b-a)
	}
	return out
}

// musicAround returns the median level of env[from:to] clipped to env; ok is
// false when fewer than least windows remain.
func musicAround(env []float32, from, to, least int) (float32, bool) {
	from, to = max(from, 0), min(to, len(env))
	if to-from < least {
		return 0, false
	}
	c := append([]float32(nil), env[from:to]...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2], true
}

// flagged returns the runs of true values in f.
func flagged(f []bool) []Span {
	var out []Span
	for i := 0; i < len(f); {
		if !f[i] {
			i++
			continue
		}
		j := i
		for j < len(f) && f[j] {
			j++
		}
		out = append(out, Span{i, j})
		i = j
	}
	return out
}

// pieces returns the stretches of music between the pauses, without pieces
// too short to be a track; nil when pauses cannot be told from music.
func pieces(env []float32) []Span {
	n := len(env)
	gaps, ok := trackGaps(env)
	if !ok {
		return nil
	}

	// Leading and trailing silence bound the audio but are not split points.
	from, to := 0, n
	if len(gaps) > 0 && gaps[0].From == 0 {
		from = gaps[0].To
		gaps = gaps[1:]
	}
	if len(gaps) > 0 && gaps[len(gaps)-1].To == n {
		to = gaps[len(gaps)-1].From
		gaps = gaps[:len(gaps)-1]
	}
	var tracks []Span
	start := from
	for _, g := range gaps {
		tracks = append(tracks, Span{start, g.From})
		start = g.To
	}
	tracks = append(tracks, Span{start, to})

	// Pieces too short to be a track (clicks, noise bursts) are dropped.
	kept := tracks[:0]
	for _, t := range tracks {
		if (t.To-t.From)*WindowMs >= minTrackMs {
			kept = append(kept, t)
		}
	}
	return kept
}

// trackGaps finds the pauses between tracks. Short pauses may be a small share
// of a side, so the floor comes from the quietest windows: tape hiss when there
// is any, otherwise digital silence (for example in an imported file).
func trackGaps(env []float32) ([]Span, bool) {
	floor, music, ok := levels(env)
	if !ok {
		return nil, false
	}
	at := func(pos float64) float32 { return float32(floor + pos*(music-floor)) }
	mid, fade, deep := at(thresholdPos), at(fadePos), at(deepPos)

	var gaps []Span
	for _, r := range runs(env, mid, 0, len(env)) {
		if (r.To-r.From)*WindowMs >= minGapMs || fadedDip(env, r, fade, deep) {
			gaps = append(gaps, r)
		}
	}
	return gaps, true
}

// levels returns the noise floor and the typical music level of env; ok is
// false when they are too close to tell pauses from music.
func levels(env []float32) (floor, music float64, ok bool) {
	if len(env) == 0 {
		return 0, 0, false
	}
	all := append([]float32(nil), env...)
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	music = float64(all[len(all)/2])
	audible := sort.Search(len(all), func(i int) bool { return all[i] > digitalSilenceDB })
	floor = float64(all[len(all)*floorPermille/1000])
	if rest := all[audible:]; len(rest) > 0 {
		if f := float64(rest[len(rest)*floorPermille/1000]); music-f >= minSpreadDB {
			floor = f
		}
	}
	return floor, music, music-floor >= minSpreadDB
}

// fadedDip reports whether the run r below the silence threshold reaches the
// hiss for deepMs and lies inside a fadeMs-long quiet stretch.
func fadedDip(env []float32, r Span, fade, deep float32) bool {
	from, to := r.From, r.To
	for from > 0 && env[from-1] < fade {
		from--
	}
	for to < len(env) && env[to] < fade {
		to++
	}
	if (to-from)*WindowMs < fadeMs {
		return false
	}
	for _, d := range runs(env, deep, r.From, r.To) {
		if (d.To-d.From)*WindowMs >= deepMs {
			return true
		}
	}
	return false
}

// runs returns the stretches of env[from:to] below thr.
func runs(env []float32, thr float32, from, to int) []Span {
	var out []Span
	for i := from; i < to; {
		if env[i] >= thr {
			i++
			continue
		}
		j := i
		for j < to && env[j] < thr {
			j++
		}
		out = append(out, Span{i, j})
		i = j
	}
	return out
}

// Silences returns the runs of at least minMs whose level is well below the
// music, including runs at the start and end. It reports false when there is
// no clear difference between music and noise floor.
func Silences(env []float32, minMs int) ([]Span, bool) {
	n := len(env)
	if n == 0 {
		return nil, false
	}
	sorted := append([]float32(nil), env...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	floor := float64(sorted[n*5/100])
	music := float64(sorted[n/2])
	if music-floor < minSpreadDB {
		return nil, false
	}
	thr := float32(floor + thresholdPos*(music-floor))
	var gaps []Span
	for i := 0; i < n; {
		if env[i] >= thr {
			i++
			continue
		}
		j := i
		for j < n && env[j] < thr {
			j++
		}
		if (j-i)*WindowMs >= minMs {
			gaps = append(gaps, Span{i, j})
		}
		i = j
	}
	return gaps, true
}
