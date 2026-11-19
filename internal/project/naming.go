package project

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/afrunt/tape2digi/internal/i18n"
)

var (
	// "1.", "01 -", "2)", "A1:" and similar list numbers with a separator.
	numberedSep = regexp.MustCompile(`^[A-Da-d]?\d{1,3}\s*[.):\-–—]+\s*`)
	// A bare number such as "A1 " or "01 ".
	numberedBare = regexp.MustCompile(`^[A-Da-d]?\d{1,3}\s+`)
)

// ParseTitleList turns pasted text into titles: one per non-empty line,
// trimmed, with a leading list number removed. A bare number without a
// separator ("01 Title") is removed only when every line starts with one, so
// a title such as "99 Luftballons" survives on its own.
func ParseTitleList(text string) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	bare := len(lines) > 0
	for _, l := range lines {
		if !numberedSep.MatchString(l) && !numberedBare.MatchString(l) {
			bare = false
		}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := l
		if loc := numberedSep.FindStringIndex(t); loc != nil && loc[1] < len(t) {
			t = t[loc[1]:]
		} else if loc := numberedBare.FindStringIndex(t); bare && loc != nil && loc[1] < len(t) {
			t = t[loc[1]:]
		}
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// NameTracks stages titles for the side's tracks in order as one history
// step and returns how many were used; tracks beyond the list keep theirs.
func (p *Project) NameTracks(key string, titles []string) (int, error) {
	return p.fillTitles(key, func(i int, cur string) (string, bool) {
		if i >= len(titles) {
			return cur, false
		}
		return titles[i], true
	})
}

// NumberTracks fills only the empty titles of the side with pattern, where
// {n} becomes the track's number counted from first, as one history step.
func (p *Project) NumberTracks(key string, first int, pattern string) error {
	_, err := p.fillTitles(key, func(i int, cur string) (string, bool) {
		if cur != "" {
			return cur, false
		}
		return strings.ReplaceAll(pattern, "{n}", strconv.Itoa(first+i)), true
	})
	return err
}

// TrackCount counts the side's complete tracks (a start followed by an end).
func TrackCount(list []Marker) int {
	n := 0
	for i := 0; i+1 < len(list); i++ {
		if list[i].Kind == MarkerStart && list[i+1].Kind == MarkerEnd {
			n++
		}
	}
	return n
}

func (p *Project) fillTitles(key string, title func(i int, cur string) (string, bool)) (int, error) {
	if !ValidSide(key) {
		return 0, i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sideLocked(key); !ok {
		return 0, i18n.New("project.sideHasNoAudio", key)
	}
	out := append([]Marker(nil), p.effectiveMarkersLocked(key)...)
	used, n, changed := 0, 0, false
	for i := range out {
		if out[i].Kind != MarkerStart || i+1 >= len(out) || out[i+1].Kind != MarkerEnd {
			continue
		}
		t, use := title(n, out[i].Title)
		n++
		if !use {
			continue
		}
		used++
		t = strings.TrimSpace(t)
		if err := checkText("project.label.trackTitle", t); err != nil {
			return 0, err
		}
		if t != out[i].Title {
			out[i].Title, changed = t, true
		}
	}
	if changed {
		p.setDraftMarkersLocked(key, out)
	}
	return used, nil
}
