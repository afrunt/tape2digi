package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSavedSide(t *testing.T) *Project {
	t.Helper()
	p, err := Create(t.TempDir(), "M")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Dir(), "side-A.wav"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSide("A", Side{Origin: OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 10000, SampleRate: 44100, Channels: 2, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMarkerRules(t *testing.T) {
	p := newSavedSide(t)
	if s, e := p.MarkerOptions("A", 1000); !s || e {
		t.Fatalf("empty list: start=%v end=%v, want start only", s, e)
	}
	if p.AddMarker("A", MarkerEnd, 1000) == nil {
		t.Fatal("end without a start must be refused")
	}
	for _, m := range []Marker{{Kind: MarkerStart, PositionMs: 1000}, {Kind: MarkerEnd, PositionMs: 3000}, {Kind: MarkerStart, PositionMs: 5000}} {
		if err := p.AddMarker("A", m.Kind, m.PositionMs); err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
	}
	if s, e := p.MarkerOptions("A", 2000); s || e {
		t.Fatalf("inside a closed track nothing fits: start=%v end=%v", s, e)
	}
	if s, e := p.MarkerOptions("A", 4000); s || e {
		t.Fatalf("between tracks nothing fits: start=%v end=%v", s, e)
	}
	if s, e := p.MarkerOptions("A", 500); s || e {
		t.Fatalf("before the first marker nothing fits: start=%v end=%v", s, e)
	}
	if s, e := p.MarkerOptions("A", 6000); s || !e {
		t.Fatalf("after open start only an end fits: start=%v end=%v", s, e)
	}
	if s, e := p.MarkerOptions("A", 3005); s || e {
		t.Fatal("markers closer than the minimum gap are refused")
	}
	if s, e := p.MarkerOptions("A", 10001); s || e {
		t.Fatal("beyond the duration is refused")
	}
	if err := p.AddMarker("A", MarkerStart, 2000); err == nil {
		t.Fatal("a start right before an end would leave the end without its start")
	}
	if err := p.AddMarker("A", MarkerStart, 7000); err == nil {
		t.Fatal("a start must not follow an open start")
	}
	if err := p.AddMarker("A", MarkerEnd, 7000); err != nil {
		t.Fatalf("an end closes the open start: %v", err)
	}
	if s, e := p.MarkerOptions("A", 8000); !s || e {
		t.Fatalf("after the last end only a start fits: start=%v end=%v", s, e)
	}
}

func TestMoveMarkerStaysBetweenNeighbours(t *testing.T) {
	p := newSavedSide(t)
	for _, m := range []Marker{{Kind: MarkerStart, PositionMs: 1000}, {Kind: MarkerEnd, PositionMs: 3000}, {Kind: MarkerStart, PositionMs: 5000}} {
		_ = p.AddMarker("A", m.Kind, m.PositionMs)
	}
	lo, hi, err := p.MarkerRange("A", 3000)
	if err != nil || lo != 1010 || hi != 4990 {
		t.Fatalf("range %d..%d %v", lo, hi, err)
	}
	if lo, hi, _ = p.MarkerRange("A", 1000); lo != 0 || hi != 2990 {
		t.Fatalf("first marker range %d..%d", lo, hi)
	}
	if lo, hi, _ = p.MarkerRange("A", 5000); lo != 3010 || hi != 10000 {
		t.Fatalf("last marker range %d..%d", lo, hi)
	}
	for _, to := range []int64{1000, 1005, 4995, 5000, 6000, -1} {
		if p.MoveMarker("A", 3000, to) == nil {
			t.Fatalf("move to %d must be refused", to)
		}
	}
	if err := p.MoveMarker("A", 3000, 4990); err != nil {
		t.Fatal(err)
	}
	if err := p.MoveMarker("A", 3000, 2000); err == nil {
		t.Fatal("the old position no longer exists")
	}
	got := p.Markers("A")
	if len(got) != 3 || got[1] != (Marker{Kind: MarkerEnd, PositionMs: 4990}) || got[0].PositionMs != 1000 || got[2].PositionMs != 5000 {
		t.Fatalf("order and kinds must be kept: %+v", got)
	}
	if !p.Dirty() {
		t.Fatal("a move is an unsaved change")
	}
	p.Discard()
	if len(p.Markers("A")) != 0 {
		t.Fatal("discard drops the move")
	}
}

func TestRemoveMarkerIsPerMarker(t *testing.T) {
	p := newSavedSide(t)
	for _, m := range []Marker{{Kind: MarkerStart, PositionMs: 1000}, {Kind: MarkerEnd, PositionMs: 3000}, {Kind: MarkerStart, PositionMs: 5000}, {Kind: MarkerEnd, PositionMs: 7000}, {Kind: MarkerStart, PositionMs: 8000}} {
		_ = p.AddMarker("A", m.Kind, m.PositionMs)
	}
	if err := p.RemoveMarker("A", 1000); err != nil || len(p.Markers("A")) != 3 {
		t.Fatalf("a start removes its end too: %v %+v", err, p.Markers("A"))
	}
	if p.RemoveMarker("A", 7000) == nil || len(p.Markers("A")) != 3 {
		t.Fatal("an end followed by a start must not be deleted")
	}
	if err := p.RemoveMarker("A", 8000); err != nil || len(p.Markers("A")) != 2 {
		t.Fatalf("an open start is removed alone: %v", err)
	}
	if err := p.RemoveMarker("A", 7000); err != nil {
		t.Fatal(err)
	}
	if got := p.Markers("A"); len(got) != 1 || got[0].Kind != MarkerStart {
		t.Fatalf("the last end removed leaves its start open: %+v", got)
	}
	if p.RemoveMarker("A", 1234) == nil {
		t.Fatal("unknown marker must be refused")
	}
}

func TestOpenCleansIllFormedMarkers(t *testing.T) {
	p := newSavedSide(t)
	bad := []Marker{{Kind: MarkerEnd, PositionMs: 100}, {Kind: MarkerStart, PositionMs: 500}, {Kind: MarkerStart, PositionMs: 505}, {Kind: MarkerStart, PositionMs: 600}, {Kind: MarkerEnd, PositionMs: 900}, {Kind: MarkerEnd, PositionMs: 950}, {Kind: MarkerStart, PositionMs: 800}, {Kind: MarkerStart, PositionMs: 20000}}
	got := cleanMarkers(bad, 10000)
	want := []Marker{{Kind: MarkerStart, PositionMs: 500}, {Kind: MarkerEnd, PositionMs: 900}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
	p.mu.Lock()
	s := p.sides["A"]
	s.Markers = bad
	p.sides["A"] = s
	p.mu.Unlock()
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := Open(p.Dir())
	if err != nil || len(q.Markers("A")) != 2 {
		t.Fatalf("open must drop the bad markers: %v %+v", err, q.Markers("A"))
	}
}

func TestMarkersStagedSavedDiscardedAndCarried(t *testing.T) {
	p := newSavedSide(t)
	_ = p.AddMarker("A", MarkerStart, 100)
	if !p.Dirty() || len(p.Info().Sides["A"].Markers) != 1 {
		t.Fatal("marker edit must be an unsaved change shown in Info")
	}
	p.Discard()
	if p.Dirty() || len(p.Markers("A")) != 0 {
		t.Fatal("discard drops marker drafts")
	}
	_ = p.AddMarker("A", MarkerStart, 100)
	_ = p.AddMarker("A", MarkerEnd, 900)
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := Open(p.Dir())
	if err != nil || len(q.Markers("A")) != 2 || q.Dirty() {
		t.Fatalf("markers must persist: %v %+v", err, q.Markers("A"))
	}
	_ = q.AddMarker("A", MarkerStart, 2000)
	copyDir := filepath.Join(t.TempDir(), "copy")
	np, err := q.SaveAs(t.Context(), copyDir, "Copy", nil)
	if err != nil || len(np.Markers("A")) != 3 {
		t.Fatalf("SaveAs carries effective markers: %v", err)
	}
	stageTake(t, q, "A", "new")
	if len(q.Markers("A")) != 0 {
		t.Fatal("a new take clears the side's markers")
	}
}

func TestEditCommitStagesCopyAndKeepsSavedFile(t *testing.T) {
	p := newSavedSide(t)
	src, dst, err := p.EditPaths("A")
	if err != nil || filepath.Base(src) != "side-A.wav" || src == dst {
		t.Fatalf("paths %s %s %v", src, dst, err)
	}
	if err := os.WriteFile(dst, []byte("louder"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.CommitEdit("A")
	if !p.Dirty() {
		t.Fatal("edit must be unsaved")
	}
	if b, _ := os.ReadFile(src); string(b) != "x" {
		t.Fatal("saved file must stay untouched")
	}
	p.Discard()
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("discard removes the staged copy")
	}
	_, dst, _ = p.EditPaths("A")
	_ = os.WriteFile(dst, []byte("louder"), 0o644)
	p.CommitEdit("A")
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(src); string(b) != "louder" {
		t.Fatal("Save applies the edited audio")
	}
}

func TestTrackDetailsFollowMarkerUndoAndSave(t *testing.T) {
	p := newSavedSide(t)
	_ = p.AddMarker("A", MarkerStart, 100)
	_ = p.AddMarker("A", MarkerEnd, 900)
	if err := p.SetTrackDetails("A", 100, " Song ", "Me", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.MoveMarker("A", 100, 200); err != nil {
		t.Fatal(err)
	}
	if m := p.Markers("A")[0]; m.Title != "Song" || m.Artist != "Me" || m.PositionMs != 200 {
		t.Fatalf("details must survive a move: %+v", m)
	}
	if _, err := p.Undo("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Undo("A"); err != nil {
		t.Fatal(err)
	}
	if m := p.Markers("A")[0]; m.Title != "" {
		t.Fatalf("undo must restore the untitled track: %+v", m)
	}
	_, _ = p.Redo("A")
	if err := p.SetTrackDetails("A", 100, "Song", "Me", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := Open(p.Dir())
	if err != nil || q.Markers("A")[0].Title != "Song" {
		t.Fatalf("details must persist: %v %+v", err, q.Markers("A"))
	}
	if err := q.SetTrackDetails("A", 100, "Song", "Me", ""); err != nil || q.Dirty() {
		t.Fatalf("unchanged call stages nothing: %v", err)
	}
	if q.SetTrackDetails("A", 900, "x", "", "") == nil || q.SetTrackDetails("A", 100, strings.Repeat("x", 201), "", "") == nil {
		t.Fatal("end marker and over-long text must be rejected")
	}
}
