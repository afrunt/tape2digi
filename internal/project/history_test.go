package project

import (
	"os"
	"path/filepath"
	"testing"
)

func markers(p *Project) []Marker { return p.Markers("A") }

func TestUndoRedoMarkers(t *testing.T) {
	p := newSavedSide(t)
	_ = p.AddMarker("A", MarkerStart, 1000)
	_ = p.AddMarker("A", MarkerEnd, 3000)
	if h := p.History("A"); !h.CanUndo || h.CanRedo {
		t.Fatalf("history %+v", h)
	}
	if _, err := p.Undo("A"); err != nil || len(markers(p)) != 1 {
		t.Fatalf("undo end: %v %v", markers(p), err)
	}
	if _, err := p.Undo("A"); err != nil || len(markers(p)) != 0 {
		t.Fatalf("undo start: %v %v", markers(p), err)
	}
	if _, err := p.Undo("A"); err == nil {
		t.Fatal("nothing left to undo")
	}
	if _, err := p.Redo("A"); err != nil || len(markers(p)) != 1 {
		t.Fatalf("redo: %v %v", markers(p), err)
	}
	if err := p.AddMarker("A", MarkerEnd, 4000); err != nil {
		t.Fatal(err)
	}
	if h := p.History("A"); h.CanRedo {
		t.Fatal("a new action clears redo")
	}
	if _, err := p.Redo("A"); err == nil {
		t.Fatal("redo must be refused")
	}
}

func TestHistoryIsCappedAndClearedBySaveAndDiscard(t *testing.T) {
	p := newSavedSide(t)
	_ = p.AddMarker("A", MarkerStart, 1000)
	_ = p.AddMarker("A", MarkerEnd, 2000)
	for i := 0; i < maxHistorySteps+20; i++ {
		if err := p.MoveMarker("A", 2000+int64(i), 2001+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for {
		if _, err := p.Undo("A"); err != nil {
			break
		}
		n++
	}
	if n != maxHistorySteps {
		t.Fatalf("undone %d steps, want %d", n, maxHistorySteps)
	}
	_ = p.AddMarker("A", MarkerStart, 9000)
	p.Discard()
	if h := p.History("A"); h.CanUndo || h.CanRedo {
		t.Fatal("Discard clears history")
	}
	_ = p.AddMarker("A", MarkerStart, 1000)
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if h := p.History("A"); h.CanUndo {
		t.Fatal("Save clears history")
	}
}

func TestUndoRedoAudioEdits(t *testing.T) {
	p := newSavedSide(t)
	read := func() string { path, _ := p.AudioPath("A"); b, _ := os.ReadFile(path); return string(b) }
	edit := func(content string) {
		_, dst, err := p.EditPaths("A")
		if err != nil {
			t.Fatal(err)
		}
		tmp := dst + ".amp"
		_ = os.WriteFile(tmp, []byte(content), 0o644)
		if err := p.ApplyEdit("A", tmp); err != nil {
			t.Fatal(err)
		}
	}
	edit("one")
	edit("two")
	if read() != "two" {
		t.Fatal(read())
	}
	if a, err := p.Undo("A"); err != nil || !a || read() != "one" {
		t.Fatalf("undo: %q %v", read(), err)
	}
	if _, err := p.Undo("A"); err != nil || read() != "x" || p.Dirty() {
		t.Fatalf("undo to the saved audio: %q dirty=%v", read(), p.Dirty())
	}
	if _, err := p.Redo("A"); err != nil || read() != "one" || !p.Dirty() {
		t.Fatalf("redo: %q", read())
	}
	if _, err := p.Redo("A"); err != nil || read() != "two" {
		t.Fatalf("redo: %q", read())
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(p.Dir(), StagingDir, "*.v*.wav")); len(left) != 0 {
		t.Fatalf("version files left: %v", left)
	}
}

func TestHistoryClearedBySideReplacementAndStaleVersionsOnOpen(t *testing.T) {
	p := newSavedSide(t)
	_ = p.AddMarker("A", MarkerStart, 1000)
	if err := p.SetSide("A", Side{Origin: OriginRecorded, DurationMs: 10000, SampleRate: 44100, Channels: 2, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	if h := p.History("A"); h.CanUndo || h.CanRedo {
		t.Fatal("SetSide clears history")
	}
	_ = p.AddMarker("A", MarkerStart, 1000)
	if err := p.RemoveSide("A"); err != nil {
		t.Fatal(err)
	}
	if h := p.History("A"); h.CanUndo || h.CanRedo {
		t.Fatal("RemoveSide clears history")
	}
	stale := filepath.Join(p.Dir(), StagingDir, "side-A.v3.wav")
	_ = os.MkdirAll(filepath.Dir(stale), 0o755)
	_ = os.WriteFile(stale, []byte("old"), 0o644)
	if _, err := Open(p.Dir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("Open removes stale version files")
	}
}

func TestCompilationChangeIsUndoable(t *testing.T) {
	p := newSavedSide(t)
	if err := p.SetDetails("Band", false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if p.LastHistory().CanUndo {
		t.Fatal("typing text is not a history step")
	}
	_ = p.AddMarker("A", MarkerStart, 1000)
	if err := p.SetDetails("Band", true, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if last := p.LastHistory(); !last.CanUndo || last.UndoSide != DetailsKey {
		t.Fatalf("last = %+v", last)
	}
	_ = p.SetDetails("Band and Co", true, "", "", "")
	if _, err := p.Undo(DetailsKey); err != nil {
		t.Fatal(err)
	}
	if info := p.Info(); info.Compilation || info.Artist != "Band and Co" {
		t.Fatalf("after undo: compilation=%v artist=%q", info.Compilation, info.Artist)
	}
	if last := p.LastHistory(); last.UndoSide != "A" || last.RedoSide != DetailsKey {
		t.Fatalf("last = %+v", last)
	}
	if _, err := p.Redo(DetailsKey); err != nil || !p.Info().Compilation {
		t.Fatalf("redo: %v", err)
	}
}

func TestDeleteSavedSideUndoRedoAndSave(t *testing.T) {
	p := newSavedSide(t)
	file := filepath.Join(p.Dir(), "side-A.wav")
	if err := p.DeleteSide("A"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Info().Sides["A"]; ok || !p.Info().Dirty || p.SideExists("A") {
		t.Fatal("deleted side must be hidden and the project dirty")
	}
	if p.DeleteSide("A") == nil || p.AddMarker("A", MarkerStart, 1000) == nil {
		t.Fatal("a deleted side accepts no changes")
	}
	if last := p.LastHistory(); last.UndoSide != "A" {
		t.Fatalf("last history %+v", last)
	}
	if audio, err := p.Undo("A"); err != nil || !audio {
		t.Fatalf("undo: %v %v", audio, err)
	}
	if _, ok := p.Info().Sides["A"]; !ok {
		t.Fatal("undo must restore the side")
	}
	if _, err := p.Redo("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("the file stays until Save")
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("Save must remove the side file")
	}
	q, err := Open(p.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Info().Sides["A"]; ok {
		t.Fatal("the descriptor must not list the side")
	}
}

func TestDeleteStagedSideUndoRestoresAudio(t *testing.T) {
	p := newSavedSide(t)
	_, dst, err := p.EditPaths("A")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(dst+".amp", []byte("edited"), 0o644)
	if err := p.ApplyEdit("A", dst+".amp"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteSide("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AudioPath("A"); err == nil {
		t.Fatal("a deleted side has no audio path")
	}
	if _, err := p.Undo("A"); err != nil {
		t.Fatal(err)
	}
	path, _ := p.AudioPath("A")
	if b, _ := os.ReadFile(path); string(b) != "edited" || !p.Info().Sides["A"].Staged {
		t.Fatalf("undo must bring back the staged edit, got %q", b)
	}
}

func TestDiscardCancelsDeletion(t *testing.T) {
	p := newSavedSide(t)
	if err := p.DeleteSide("A"); err != nil {
		t.Fatal(err)
	}
	p.Discard()
	if _, ok := p.Info().Sides["A"]; !ok || p.Info().Dirty {
		t.Fatal("Discard must bring the side back")
	}
}
