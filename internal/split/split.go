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
)

// Span is a stretch of envelope windows, To exclusive.
type Span struct{ From, To int }

// Detect returns alternating start and end markers for the tracks found in
// env (dBFS per WindowMs window), or nil when no confident split of at least
// two tracks exists.
func Detect(env []float32) []project.Marker {
	n := len(env)
	gaps, ok := Silences(env, minGapMs)
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
	if len(kept) < 2 {
		return nil
	}

	total := int64(n) * WindowMs
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
