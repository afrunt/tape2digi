package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/afrunt/tape2digi/internal/i18n"
)

// Marker kinds.
const (
	MarkerStart = "start"
	MarkerEnd   = "end"
)

// minMarkerGapMs is the smallest distance between two markers.
const minMarkerGapMs = 10

// Marker is a track start or end position inside a side.
type Marker struct {
	Kind       string `json:"kind"`
	PositionMs int64  `json:"positionMs"`
	// Title, Artist and AlbumArtist describe the track that a start marker
	// opens; an empty artist follows the project.
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	AlbumArtist string `json:"albumArtist,omitempty"`
}

// markerAllowed reports whether a marker of the given kind may be inserted at
// ms into the ordered list. Markers strictly alternate start, end, start, end:
// a start goes only after the last end (or into an empty list) and an end only
// after the last, open start.
func markerAllowed(list []Marker, kind string, ms, durationMs int64) bool {
	if ms < 0 || ms > durationMs {
		return false
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].PositionMs >= ms })
	if i < len(list) && list[i].PositionMs-ms < minMarkerGapMs {
		return false
	}
	if i > 0 && ms-list[i-1].PositionMs < minMarkerGapMs {
		return false
	}
	if i != len(list) {
		return false
	}
	prevStart := i > 0 && list[i-1].Kind == MarkerStart
	switch kind {
	case MarkerStart:
		return !prevStart
	case MarkerEnd:
		return prevStart
	}
	return false
}

func insertMarker(list []Marker, m Marker) []Marker {
	i := sort.Search(len(list), func(i int) bool { return list[i].PositionMs >= m.PositionMs })
	out := make([]Marker, 0, len(list)+1)
	out = append(out, list[:i]...)
	out = append(out, m)
	return append(out, list[i:]...)
}

// markerRange returns the positions the marker at ms may be moved to: between
// its neighbours (kept the minimum gap apart) or the ends of the side.
func markerRange(list []Marker, ms, durationMs int64) (lo, hi int64, idx int, ok bool) {
	for i, m := range list {
		if m.PositionMs != ms {
			continue
		}
		lo, hi = 0, durationMs
		if i > 0 {
			lo = list[i-1].PositionMs + minMarkerGapMs
		}
		if i+1 < len(list) {
			hi = list[i+1].PositionMs - minMarkerGapMs
		}
		return lo, hi, i, lo <= hi
	}
	return 0, 0, -1, false
}

// removeMarker removes the marker at ms; a start also removes the end that
// directly follows it; an end is removed only when it is the last marker and
// leaves its start open.
func removeMarker(list []Marker, ms int64) (out []Marker, found, allowed bool) {
	for i, m := range list {
		if m.PositionMs != ms {
			continue
		}
		if m.Kind == MarkerEnd && i != len(list)-1 {
			return list, true, false
		}
		to := i + 1
		if m.Kind == MarkerStart && i+1 < len(list) && list[i+1].Kind == MarkerEnd {
			to = i + 2
		}
		out = append([]Marker{}, list[:i]...)
		return append(out, list[to:]...), true, true
	}
	return list, false, false
}

// cleanMarkers drops markers that make the list ill-formed: out of range,
// unsorted or too close to the previous one, an end without a start or a start
// right after another start.
func cleanMarkers(list []Marker, durationMs int64) []Marker {
	var out []Marker
	for _, m := range list {
		if m.PositionMs < 0 || m.PositionMs > durationMs || (m.Kind != MarkerStart && m.Kind != MarkerEnd) {
			continue
		}
		if n := len(out); n > 0 {
			if m.PositionMs-out[n-1].PositionMs < minMarkerGapMs {
				continue
			}
			if m.Kind == MarkerEnd && out[n-1].Kind != MarkerStart {
				continue
			}
			if m.Kind == MarkerStart && out[n-1].Kind == MarkerStart {
				continue
			}
		} else if m.Kind == MarkerEnd {
			continue
		}
		out = append(out, m)
	}
	return out
}

// effectiveMarkersLocked returns the staged markers when the side has a draft,
// otherwise those of the pending or saved side.
func (p *Project) effectiveMarkersLocked(key string) []Marker {
	if d, ok := p.draftMarkers[key]; ok {
		return d
	}
	if s, ok := p.staged[key]; ok {
		return s.Markers
	}
	return p.sides[key].Markers
}

func (p *Project) sideLocked(key string) (Side, bool) {
	if p.deleted[key] {
		return Side{}, false
	}
	if s, ok := p.staged[key]; ok {
		return s, true
	}
	s, ok := p.sides[key]
	return s, ok
}

// Markers returns the side's current track markers.
func (p *Project) Markers(key string) []Marker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Marker(nil), p.effectiveMarkersLocked(key)...)
}

// MarkerOptions reports which markers may be inserted at ms.
func (p *Project) MarkerOptions(key string, ms int64) (start, end bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sideLocked(key)
	if !ok {
		return false, false
	}
	list := p.effectiveMarkersLocked(key)
	return markerAllowed(list, MarkerStart, ms, s.DurationMs), markerAllowed(list, MarkerEnd, ms, s.DurationMs)
}

func (p *Project) setDraftMarkersLocked(key string, list []Marker) {
	if p.draftMarkers == nil {
		p.draftMarkers = map[string][]Marker{}
	}
	st := step{after: copyMarkers(list)}
	if old, ok := p.draftMarkers[key]; ok {
		st.before = copyMarkers(old)
	}
	p.draftMarkers[key] = list
	p.pushLocked(key, st)
}

// AddMarker inserts a marker into a staged draft written by Save.
func (p *Project) AddMarker(key, kind string, ms int64) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sideLocked(key)
	if !ok {
		return i18n.New("project.sideHasNoAudio", key)
	}
	list := p.effectiveMarkersLocked(key)
	if !markerAllowed(list, kind, ms, s.DurationMs) {
		return i18n.New("project.markerNotAllowed")
	}
	p.setDraftMarkersLocked(key, insertMarker(list, Marker{Kind: kind, PositionMs: ms}))
	return nil
}

// ReplaceMarkers stages list as the side's complete marker set (one history step).
func (p *Project) ReplaceMarkers(key string, list []Marker) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sideLocked(key)
	if !ok {
		return i18n.New("project.sideHasNoAudio", key)
	}
	p.setDraftMarkersLocked(key, cleanMarkers(list, s.DurationMs))
	return nil
}

// MarkerRange returns the lowest and highest position the marker at ms may be
// moved to.
func (p *Project) MarkerRange(key string, ms int64) (lo, hi int64, err error) {
	if !ValidSide(key) {
		return 0, 0, i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sideLocked(key)
	if !ok {
		return 0, 0, i18n.New("project.sideHasNoAudio", key)
	}
	lo, hi, _, found := markerRange(p.effectiveMarkersLocked(key), ms, s.DurationMs)
	if !found {
		return 0, 0, i18n.New("project.markerNotFound")
	}
	return lo, hi, nil
}

// MoveMarker moves the marker at fromMs to toMs without passing a neighbour.
func (p *Project) MoveMarker(key string, fromMs, toMs int64) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sideLocked(key)
	if !ok {
		return i18n.New("project.sideHasNoAudio", key)
	}
	list := p.effectiveMarkersLocked(key)
	lo, hi, i, found := markerRange(list, fromMs, s.DurationMs)
	if i < 0 {
		return i18n.New("project.markerNotFound")
	}
	if !found || toMs < lo || toMs > hi {
		return i18n.New("project.markerMoveNotAllowed")
	}
	if toMs == fromMs {
		return nil
	}
	out := append([]Marker(nil), list...)
	out[i].PositionMs = toMs
	p.setDraftMarkersLocked(key, out)
	return nil
}

// SetTrackDetails stages the title, artist and album artist of the track that
// starts at startMs as one undoable step; an unchanged call stages nothing.
func (p *Project) SetTrackDetails(key string, startMs int64, title, artist, albumArtist string) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	title, artist, albumArtist = strings.TrimSpace(title), strings.TrimSpace(artist), strings.TrimSpace(albumArtist)
	for _, f := range []struct {
		label i18n.Key
		v     string
	}{{"project.label.trackTitle", title}, {"project.label.trackArtist", artist}, {"project.label.albumArtist", albumArtist}} {
		if err := checkText(f.label, f.v); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sideLocked(key); !ok {
		return i18n.New("project.sideHasNoAudio", key)
	}
	list := p.effectiveMarkersLocked(key)
	i := -1
	for n, m := range list {
		if m.Kind == MarkerStart && m.PositionMs == startMs {
			i = n
		}
	}
	if i < 0 {
		return i18n.New("project.markerNotFound")
	}
	if m := list[i]; m.Title == title && m.Artist == artist && m.AlbumArtist == albumArtist {
		return nil
	}
	out := append([]Marker(nil), list...)
	out[i].Title, out[i].Artist, out[i].AlbumArtist = title, artist, albumArtist
	p.setDraftMarkersLocked(key, out)
	return nil
}

// RemoveMarker removes the marker at ms (a start also removes its end).
func (p *Project) RemoveMarker(key string, ms int64) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, found, allowed := removeMarker(p.effectiveMarkersLocked(key), ms)
	if !found {
		return i18n.New("project.markerNotFound")
	}
	if !allowed {
		return i18n.New("project.markerEndNotLast")
	}
	p.setDraftMarkersLocked(key, list)
	return nil
}

// EditPaths returns the file an audio edit reads and the staged file it
// writes. The side's own file is never written before Save.
func (p *Project) EditPaths(key string) (src, dst string, err error) {
	if !ValidSide(key) {
		return "", "", i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	_, ok := p.sideLocked(key)
	p.mu.Unlock()
	if !ok {
		return "", "", i18n.New("project.sideHasNoAudio", key)
	}
	if src, err = p.AudioPath(key); err != nil {
		return "", "", err
	}
	if dst, err = p.stagedPath(key); err != nil {
		return "", "", err
	}
	if _, err = os.Stat(src); err != nil {
		return "", "", i18n.New("project.sideHasNoAudio", key)
	}
	return src, dst, os.MkdirAll(filepath.Dir(dst), 0o755)
}

// CommitEdit registers the edited staged file written by an audio edit; a side
// that was only saved so far becomes a pending (staged) side.
func (p *Project) CommitEdit(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.staged[key]; ok {
		return
	}
	s := p.sides[key]
	s.Staged, s.Missing = true, false
	if p.staged == nil {
		p.staged = map[string]Side{}
	}
	p.staged[key] = s
}
