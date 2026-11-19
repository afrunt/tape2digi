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
)

// Span is a stretch of envelope windows, To exclusive.
type Span struct{ From, To int }

// Detect returns alternating start and end markers for the tracks found in
// env (dBFS per WindowMs window), or nil when no confident split of at least
// two tracks exists.
func Detect(env []float32) []project.Marker {
	kept := pieces(env)
	if len(kept) < 2 {
		return nil
	}

	total := int64(len(env)) * WindowMs
	out := make([]project.Marker, 0, 2*len(kept))
	for _, t := range kept {
		s := int64(t.From)*WindowMs - padMs
		e := int64(t.To)*WindowMs + padMs
		if s < 0 {
			s = 0
		}
		if e > total {
			e = total
		}
		out = append(out, project.Marker{Kind: project.MarkerStart, PositionMs: s}, project.Marker{Kind: project.MarkerEnd, PositionMs: e})
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
