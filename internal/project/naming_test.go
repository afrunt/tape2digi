package project

import (
	"slices"
	"testing"
)

func TestParseTitleList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"1. One\r\n\n  02 - Two  \n3) Three\nA4: Four\n", []string{"One", "Two", "Three", "Four"}},
		{"A1 Intro\nA2 Song\n", []string{"Intro", "Song"}},
		{"99 Luftballons\nHello\n", []string{"99 Luftballons", "Hello"}},
		{"  \n\n", []string{}},
		{"7", []string{"7"}},
	}
	for _, c := range cases {
		if got := ParseTitleList(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestNameAndNumberTracks(t *testing.T) {
	p := newSavedSide(t)
	for _, ms := range []int64{100, 300, 500} {
		_ = p.AddMarker("A", MarkerStart, ms)
		_ = p.AddMarker("A", MarkerEnd, ms+100)
	}
	_ = p.SetTrackDetails("A", 500, "Kept", "", "")
	used, err := p.NameTracks("A", []string{"One"})
	if err != nil || used != 1 {
		t.Fatalf("used %d %v", used, err)
	}
	if err := p.NumberTracks("A", 5, "Track {n}"); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range p.Markers("A") {
		if m.Kind == MarkerStart {
			titles = append(titles, m.Title)
		}
	}
	if !slices.Equal(titles, []string{"One", "Track 6", "Kept"}) {
		t.Fatalf("titles %q", titles)
	}
	if used, _ := p.NameTracks("A", []string{"a", "b", "c", "d"}); used != 3 {
		t.Fatalf("extra lines are not used: %d", used)
	}
	// Three steps: the list, the numbering and the second list; one undo each.
	for i := 0; i < 3; i++ {
		if _, err := p.Undo("A"); err != nil {
			t.Fatal(err)
		}
	}
	if m := p.Markers("A"); m[0].Title != "" || m[2].Title != "" || m[4].Title != "Kept" {
		t.Fatalf("each fill is one undo step: %+v", m)
	}
}
