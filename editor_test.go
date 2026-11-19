package main

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afrunt/tape2digi/internal/appdata"
	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/recorder"
	"github.com/afrunt/tape2digi/internal/wav"
)

func editorApp(t *testing.T) (*App, string) {
	t.Helper()
	p, err := project.Create(t.TempDir(), "E")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Dir(), "side-A.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	format := wav.Format{SampleRate: 8000, Channels: 2, BitDepth: 16}
	w, err := wav.NewWriter(f, format)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8000*4)
	for i := 0; i < len(data); i += 4 {
		data[i+1] = 0x10 // left sample 0x1000
		data[i+3] = 0x08 // right sample 0x0800
	}
	_, _ = w.Write(data)
	_ = w.Close()
	_ = f.Close()
	if err := p.SetSide("A", project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 1000, SampleRate: 8000, Channels: 2, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	src := &audio.FakeSource{}
	return &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}, path
}

func TestAmplifyIsStagedUntilSaveAndOnlyOnce(t *testing.T) {
	app, path := editorApp(t)
	before, _ := os.ReadFile(path)
	if g, err := app.SideGainInfo("A"); err != nil || !g.Available || g.DB < 17 {
		t.Fatalf("gain %+v %v", g, err)
	}
	if _, err := app.AmplifySide("A"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("saved file changed before Save")
	}
	if g, _ := app.SideGainInfo("A"); g.Available {
		t.Fatalf("second amplify must be unavailable: %+v", g)
	}
	if _, err := app.AmplifySide("A"); err == nil {
		t.Fatal("second amplify must fail")
	}
	if _, err := app.DiscardChanges(); err != nil {
		t.Fatal(err)
	}
	if g, _ := app.SideGainInfo("A"); !g.Available {
		t.Fatal("discard restores the original level")
	}
	if _, err := app.AmplifySide("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveProject(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); bytes.Equal(before, after) {
		t.Fatal("Save must apply the amplified audio")
	}
}

func TestEditorRefusesDuringJobAndBadMarkers(t *testing.T) {
	app, _ := editorApp(t)
	_, done, err := app.beginJob()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveProject(); err == nil {
		t.Fatal("Save must be refused during a job")
	}
	if _, err := app.DiscardChanges(); err == nil {
		t.Fatal("Discard must be refused during a job")
	}
	if _, err := app.AddMarker("A", "start", 100); err == nil {
		t.Fatal("marker edits must be refused during a job")
	}
	done()
	if _, err := app.AddMarker("A", "end", 100); err == nil {
		t.Fatal("end marker without a start must be refused")
	}
	if c, _ := app.MarkerOptions("A", 100); !c.Start || c.End {
		t.Fatalf("options %+v", c)
	}
	if _, err := app.AddMarker("A", "start", 100); err != nil {
		t.Fatal(err)
	}
	if c, _ := app.MarkerOptions("A", 500); c.Start || !c.End {
		t.Fatalf("options %+v", c)
	}
}

func TestWaveformShape(t *testing.T) {
	app, _ := editorApp(t)
	w, err := app.SideWaveform("A", 100)
	if err != nil || w.Channels != 2 || len(w.Max[0]) != 100 || w.DurationMs != 1000 {
		t.Fatalf("%+v %v", w, err)
	}
	if w.Max[0][50] <= w.Max[1][50] {
		t.Fatal("left is louder than right")
	}
}

func TestMoveMarkerBinding(t *testing.T) {
	app, _ := editorApp(t)
	for _, m := range []struct {
		kind string
		ms   int64
	}{{"start", 100}, {"end", 400}, {"start", 700}} {
		if _, err := app.AddMarker("A", m.kind, m.ms); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := app.MarkerRange("A", 400); err != nil || r.Min != 110 || r.Max != 690 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := app.MoveMarker("A", 400, 700); err == nil {
		t.Fatal("moving onto a neighbour must be refused")
	}
	if _, err := app.MoveMarker("A", 400, 500); err != nil {
		t.Fatal(err)
	}
	_, done, _ := app.beginJob()
	if _, err := app.MoveMarker("A", 500, 450); err == nil {
		t.Fatal("moves must be refused during a job")
	}
	done()
	if _, err := app.RemoveMarker("A", 100); err != nil {
		t.Fatal(err)
	}
	if got := app.proj.Markers("A"); len(got) != 1 || got[0].PositionMs != 700 {
		t.Fatalf("%+v", got)
	}
}

func TestUndoRedoAmplifyThroughBindings(t *testing.T) {
	app, _ := editorApp(t)
	if _, err := app.AmplifySide("A"); err != nil {
		t.Fatal(err)
	}
	if g, _ := app.SideGainInfo("A"); g.Available {
		t.Fatal("amplified side has no more gain")
	}
	if _, err := app.UndoSide("A"); err != nil {
		t.Fatal(err)
	}
	if g, _ := app.SideGainInfo("A"); !g.Available {
		t.Fatal("undo restores the original level")
	}
	if h, _ := app.EditHistory("A"); h.CanUndo || !h.CanRedo {
		t.Fatalf("history %+v", h)
	}
	if _, err := app.RedoSide("A"); err != nil {
		t.Fatal(err)
	}
	if g, _ := app.SideGainInfo("A"); g.Available {
		t.Fatal("redo amplifies again")
	}
	_, done, _ := app.beginJob()
	defer done()
	if _, err := app.UndoSide("A"); err == nil {
		t.Fatal("undo must be refused during a job")
	}
}

func TestSplitSideReplacesMarkersInOneUndoableStep(t *testing.T) {
	p, err := project.Create(t.TempDir(), "S")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(p.Dir(), "side-A.wav"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(f, wav.Format{SampleRate: 8000, Channels: 1, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	// 3 s silence, 15 s tone, 3 s silence, 15 s tone, 3 s silence.
	for _, loud := range []bool{false, true, false, true, false} {
		sec := 3
		if loud {
			sec = 15
		}
		data := make([]byte, 8000*2*sec)
		if loud {
			for i := 0; i < len(data); i += 2 {
				data[i+1] = 0x20
			}
		}
		_, _ = w.Write(data)
	}
	_ = w.Close()
	_ = f.Close()
	if err := p.SetSide("A", project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 39000, SampleRate: 8000, Channels: 1, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	src := &audio.FakeSource{}
	app := &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}

	plan, err := app.SideSplitPlan("A")
	if err != nil || !plan.Available || plan.Tracks != 2 {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	if _, err := app.AddMarker("A", "start", 5000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SplitSide("A"); err != nil {
		t.Fatal(err)
	}
	if got := p.Markers("A"); len(got) != 4 || got[0].PositionMs != 2750 || got[3].PositionMs != 36250 {
		t.Fatalf("markers %v", got)
	}
	if _, err := app.UndoSide("A"); err != nil {
		t.Fatal(err)
	}
	if got := p.Markers("A"); len(got) != 1 || got[0].PositionMs != 5000 {
		t.Fatalf("markers after undo %v", got)
	}
}

func TestDenoiseSideStagesCopyAndUndoRestores(t *testing.T) {
	p, err := project.Create(t.TempDir(), "N")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Dir(), "side-A.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(f, wav.Format{SampleRate: 8000, Channels: 1, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(2))
	// 4 s noise, 12 s tone with noise, 4 s noise.
	for _, secs := range []int{4, 12, 4} {
		data := make([]byte, 8000*2*secs)
		for i := 0; i < len(data); i += 2 {
			v := int16(rng.NormFloat64() * 120)
			if secs == 12 {
				v += int16(9000 * math.Sin(float64(i/2)*0.3))
			}
			data[i], data[i+1] = byte(v), byte(uint16(v)>>8)
		}
		_, _ = w.Write(data)
	}
	_ = w.Close()
	_ = f.Close()
	if err := p.SetSide("A", project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 20000, SampleRate: 8000, Channels: 1, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	src := &audio.FakeSource{}
	app := &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}

	if plan, err := app.SideNoisePlan("A"); err != nil || !plan.Available {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	before, _ := os.ReadFile(path)
	if _, err := app.DenoiseSide("A"); err != nil {
		t.Fatal(err)
	}
	staged, _ := p.AudioPath("A")
	if after, _ := os.ReadFile(staged); bytes.Equal(before, after) || len(after) != len(before) {
		t.Fatalf("staged copy not changed or length differs (%d vs %d)", len(after), len(before))
	}
	if _, err := app.UndoSide("A"); err != nil {
		t.Fatal(err)
	}
	if restored, _ := p.AudioPath("A"); restored != "" {
		if got, _ := os.ReadFile(restored); !bytes.Equal(before, got) {
			t.Fatal("undo did not restore the original audio")
		}
	}
}

func TestResetMarkersIsOneUndoableStep(t *testing.T) {
	p, err := project.Create(t.TempDir(), "R")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetSide("A", project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 60000, SampleRate: 8000, Channels: 1, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	src := &audio.FakeSource{}
	app := &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}
	for _, m := range []struct {
		kind string
		ms   int64
	}{{"start", 1000}, {"end", 20000}} {
		if _, err := app.AddMarker("A", m.kind, m.ms); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.ResetMarkers("A"); err != nil {
		t.Fatal(err)
	}
	if got := p.Markers("A"); len(got) != 0 {
		t.Fatalf("markers after reset %v", got)
	}
	if _, err := app.UndoSide("A"); err != nil {
		t.Fatal(err)
	}
	if got := p.Markers("A"); len(got) != 2 {
		t.Fatalf("markers after undo %v", got)
	}
}

func TestUndoLastRedoLastFollowTheMostRecentSide(t *testing.T) {
	p, err := project.Create(t.TempDir(), "L")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"A", "B"} {
		if err := p.SetSide(k, project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 60000, SampleRate: 8000, Channels: 1, BitDepth: 16}); err != nil {
			t.Fatal(err)
		}
	}
	src := &audio.FakeSource{}
	app := &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}
	if _, err := app.AddMarker("A", "start", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddMarker("B", "start", 2000); err != nil {
		t.Fatal(err)
	}
	if h, _ := app.EditHistoryLast(); !h.CanUndo || h.UndoSide != "B" {
		t.Fatalf("history %+v", h)
	}
	_, _ = app.UndoLast()
	if len(p.Markers("B")) != 0 || len(p.Markers("A")) != 1 {
		t.Fatalf("first undo must revert B: A=%v B=%v", p.Markers("A"), p.Markers("B"))
	}
	_, _ = app.UndoLast()
	if len(p.Markers("A")) != 0 {
		t.Fatal("second undo must revert A")
	}
	_, _ = app.RedoLast()
	if len(p.Markers("A")) != 1 || len(p.Markers("B")) != 0 {
		t.Fatalf("first redo must reapply A: A=%v B=%v", p.Markers("A"), p.Markers("B"))
	}
	_, _ = app.RedoLast()
	if len(p.Markers("B")) != 1 {
		t.Fatal("second redo must reapply B")
	}
	if h, _ := app.EditHistoryLast(); h.CanRedo {
		t.Fatalf("nothing left to redo: %+v", h)
	}
}

func TestRepairSideAppliesMonoAndUndoRestores(t *testing.T) {
	p, err := project.Create(t.TempDir(), "M")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Dir(), "side-A.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wav.NewWriter(f, wav.Format{SampleRate: 8000, Channels: 2, BitDepth: 16})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8000*4*3)
	for i := 0; i < len(data); i += 4 {
		l := int16(8000 * math.Sin(float64(i/4)*0.2))
		r := l / 2
		data[i], data[i+1] = byte(l), byte(uint16(l)>>8)
		data[i+2], data[i+3] = byte(r), byte(uint16(r)>>8)
	}
	_, _ = w.Write(data)
	_ = w.Close()
	_ = f.Close()
	if err := p.SetSide("A", project.Side{Origin: project.OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 3000, SampleRate: 8000, Channels: 2, BitDepth: 16}); err != nil {
		t.Fatal(err)
	}
	src := &audio.FakeSource{}
	app := &App{proj: p, src: src, rec: recorder.New(src, recorder.Handlers{}), recents: project.NewRecents(appdata.Dir{Path: t.TempDir()})}

	plan, err := app.SideRepairPlan("A")
	if err != nil || plan.Channels != 2 || !plan.BalanceFix {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	if _, err := app.RepairSide("A", "hum", "", 0); err == nil {
		t.Fatal("hum repair offered without hum")
	}
	before, _ := os.ReadFile(path)
	if _, err := app.RepairSide("A", "mono", "left", 0); err != nil {
		t.Fatal(err)
	}
	staged, _ := p.AudioPath("A")
	after, _ := os.ReadFile(staged)
	if len(after) != len(before) || !bytes.Equal(after[44:46], after[46:48]) || bytes.Equal(before, after) {
		t.Fatal("mono left did not copy the left channel")
	}
	if plan, _ := app.SideRepairPlan("A"); plan.BalanceFix {
		t.Fatal("the cached analysis must be dropped after an edit")
	}
	if _, err := app.UndoSide("A"); err != nil {
		t.Fatal(err)
	}
	restored, _ := p.AudioPath("A")
	if got, _ := os.ReadFile(restored); !bytes.Equal(before, got) {
		t.Fatal("undo did not restore the original audio")
	}
}
