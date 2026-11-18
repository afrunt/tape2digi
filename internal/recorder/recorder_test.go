package recorder

import (
	"bytes"
	"context"
	"errors"
	"github.com/afrunt/tape2digi/internal/i18n"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/project"
)

var stereo16 = audio.Format{SampleRate: 1000, Channels: 2, BitDepth: 16}

type events struct {
	mu     sync.Mutex
	levels [][]audio.ChannelLevel
	errs   []string
}

func (e *events) handlers() Handlers {
	return Handlers{
		OnLevel: func(l []audio.ChannelLevel) { e.mu.Lock(); e.levels = append(e.levels, l); e.mu.Unlock() },
		OnError: func(m string) { e.mu.Lock(); e.errs = append(e.errs, m); e.mu.Unlock() },
	}
}

func (e *events) errors() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.errs...)
}

func setup(t *testing.T) (*Service, *audio.FakeSource, *project.Project, *events) {
	t.Helper()
	src := &audio.FakeSource{Devs: []audio.Device{{ID: "d1", Name: "USB", IsDefault: true}}}
	ev := &events{}
	svc := New(src, ev.handlers())
	svc.LevelInterval = 0
	p, err := project.Create(t.TempDir(), "Test")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetProject(p); err != nil {
		t.Fatal(err)
	}
	return svc, src, p, ev
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func frames(n int) []byte { return make([]byte, n*4) } // stereo 16-bit

func TestIllegalTransitionsReturnErrors(t *testing.T) {
	svc, _, _, _ := setup(t)
	if err := svc.Start("A", false); err == nil {
		t.Fatal("start without input")
	}
	if _, err := svc.Stop(); err == nil {
		t.Fatal("stop while idle")
	}
	if err := svc.Pause(); err == nil || svc.Resume() == nil {
		t.Fatal("pause/resume while idle")
	}
	if err := svc.OpenInput("d1", "USB", stereo16); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("A", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("A", false); err == nil {
		t.Fatal("start while recording")
	}
	if err := svc.OpenInput("d1", "USB", stereo16); err == nil {
		t.Fatal("device switch while recording")
	}
	if _, err := svc.Stop(); !errors.Is(err, ErrEmptyTake) {
		t.Fatal(err)
	}
	if _, err := svc.Stop(); err == nil {
		t.Fatal("double stop")
	}
}

func TestStartRequiresProject(t *testing.T) {
	src := &audio.FakeSource{}
	svc := New(src, Handlers{})
	_ = svc.OpenInput("", "x", stereo16)
	if err := svc.Start("A", false); err == nil {
		t.Fatal("start without project")
	}
}

func TestRecordPauseResumeStop(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(100))
	waitFor(t, "first chunk", func() bool { return svc.Status().SizeBytes == 400 })
	_ = svc.Pause()
	_ = src.Emit(frames(100))
	time.Sleep(50 * time.Millisecond)
	if got := svc.Status().SizeBytes; got != 400 {
		t.Fatalf("paused data written: %d", got)
	}
	_ = svc.Resume()
	_ = src.Emit(frames(100))
	waitFor(t, "resumed chunk", func() bool { return svc.Status().SizeBytes == 800 })
	rec, err := svc.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if rec.DurationMs != 200 || rec.Device != "USB" || rec.Origin != project.OriginRecorded {
		t.Fatalf("%+v", rec)
	}
	fi, err := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav"))
	if err != nil || fi.Size() != 44+800 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.rec")); err == nil {
		t.Fatal("part file left behind")
	}
	if info := p.Info(); len(info.Sides) != 1 || info.Sides["A"].File != "side-A.wav" {
		t.Fatalf("descriptor: %+v", info)
	}
	if svc.Status().State != StateMetering {
		t.Fatalf("state %s", svc.Status().State)
	}
}

func TestLevelsOneEntryPerChannel(t *testing.T) {
	svc, src, _, ev := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = src.Emit(frames(10))
	waitFor(t, "level event", func() bool { ev.mu.Lock(); defer ev.mu.Unlock(); return len(ev.levels) > 0 })
	if n := len(ev.levels[0]); n != 2 {
		t.Fatalf("stereo channels %d", n)
	}
	_ = svc.OpenInput("d1", "USB", audio.Format{SampleRate: 1000, Channels: 1, BitDepth: 16})
	ev.mu.Lock()
	ev.levels = nil
	ev.mu.Unlock()
	_ = src.Emit(make([]byte, 20))
	waitFor(t, "mono level", func() bool { ev.mu.Lock(); defer ev.mu.Unlock(); return len(ev.levels) > 0 })
	if n := len(ev.levels[0]); n != 1 {
		t.Fatalf("mono channels %d", n)
	}
}

func TestDeviceLostMidRecording(t *testing.T) {
	svc, src, p, ev := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(50))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 200 })
	src.StopCapture()
	waitFor(t, "idle", func() bool { return svc.Status().State == StateIdle })
	waitFor(t, "error event", func() bool { return len(ev.errors()) > 0 })
	if len(p.Info().Sides) != 1 {
		t.Fatal("partial recording must be kept in the project")
	}
}

func TestSizeLimitStopsRecordingCleanly(t *testing.T) {
	svc, src, p, ev := setup(t)
	svc.maxData = 400
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(150)) // 600 bytes > cap
	waitFor(t, "stop at cap", func() bool { return svc.Status().State == StateMetering })
	errs := ev.errors()
	if len(errs) == 0 || !strings.Contains(errs[0], "4 GiB") {
		t.Fatalf("errors: %v", errs)
	}
	fi, _ := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav"))
	if fi.Size() != 44+400 {
		t.Fatalf("size %d", fi.Size())
	}
}

func TestDescriptorFailureKeepsFile(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(10))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 40 })
	if err := os.Chmod(p.Dir(), 0o555); err != nil {
		t.Skip("chmod unsupported")
	}
	defer os.Chmod(p.Dir(), 0o755)
	_, err := svc.Stop()
	if err == nil {
		t.Skip("directory permissions not enforced (running as root?)")
	}
	if !p.HasPart("A") {
		t.Fatal("the take must stay on disk as a part file")
	}
}

func TestMonitorFailureNeverAffectsRecording(t *testing.T) {
	svc, src, _, _ := setup(t)
	src.PlaybackErr = os.ErrPermission
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	if err := svc.SetMonitor(true); err == nil {
		t.Fatal("expected monitor error")
	}
	_ = src.Emit(frames(100))
	waitFor(t, "recorded despite monitor failure", func() bool { return svc.Status().SizeBytes == 400 })
	if svc.Status().Monitor {
		t.Fatal("monitor should be off")
	}
	_, _ = svc.Stop()
}

func TestMonitorOffByDefaultAndTogglesIndependently(t *testing.T) {
	svc, src, _, _ := setup(t)
	if err := svc.SetMonitor(true); err == nil {
		t.Fatal("monitor without input must fail")
	}
	_ = svc.OpenInput("d1", "USB", stereo16)
	if svc.Status().Monitor || src.PlayOpen {
		t.Fatal("monitor must be off by default")
	}
	if err := svc.SetMonitor(true); err != nil || !svc.Status().Monitor || !src.PlayOpen {
		t.Fatalf("enable: %v", err)
	}
	_ = svc.SetMonitor(false)
	if svc.Status().Monitor || src.PlayOpen {
		t.Fatal("disable failed")
	}
	_ = svc.SetMonitor(true)
	src.StopPlayback()
	waitFor(t, "monitor lost", func() bool { return !svc.Status().Monitor })
}

func withOutputs(src *audio.FakeSource) {
	src.PlayDevs = []audio.Device{{ID: "o1", Name: "Speakers", IsDefault: true}, {ID: "o2", Name: "Headphones"}}
}

func TestMonitorOutputDefaultNamedAndFallback(t *testing.T) {
	svc, src, _, _ := setup(t)
	withOutputs(src)
	_ = svc.OpenInput("d1", "USB", stereo16)
	if err := svc.SetMonitor(true); err != nil || src.LastPlayDevice != "" {
		t.Fatalf("default output must be requested as empty id, got %q (%v)", src.LastPlayDevice, err)
	}
	if err := svc.SetMonitorDevice("Headphones"); err != nil || src.LastPlayDevice != "o2" || !svc.Status().Monitor {
		t.Fatalf("switch: %q %v", src.LastPlayDevice, err)
	}
	if err := svc.SetMonitorDevice("Gone"); err != nil || src.LastPlayDevice != "" || !svc.Status().Monitor {
		t.Fatalf("missing device must fall back to default: %q %v", src.LastPlayDevice, err)
	}
}

func TestMonitorOutputChoiceSurvivesInputReopenAndSwitchIsNotLoss(t *testing.T) {
	svc, src, _, _ := setup(t)
	withOutputs(src)
	src.StopOnClose = true
	_ = svc.SetMonitorDevice("Headphones") // chosen before any input exists
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.SetMonitor(true)
	if src.LastPlayDevice != "o2" {
		t.Fatalf("stored choice not used: %q", src.LastPlayDevice)
	}
	_ = svc.SetMonitorDevice("Speakers")
	time.Sleep(50 * time.Millisecond) // a stale stop would arrive asynchronously
	if !svc.Status().Monitor {
		t.Fatal("closing the old stream must not turn the monitor off")
	}
	_ = svc.OpenInput("d1", "USB", stereo16) // monitor is rebuilt, choice kept
	if !svc.Status().Monitor || src.LastPlayDevice != "o1" {
		t.Fatalf("after reopen: monitor=%v device=%q", svc.Status().Monitor, src.LastPlayDevice)
	}
	src.StopPlayback()
	waitFor(t, "genuine loss of the current stream", func() bool { return !svc.Status().Monitor })
}

func TestMonitorOutputFailureStopsMonitorKeepsRecording(t *testing.T) {
	svc, src, _, _ := setup(t)
	withOutputs(src)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = svc.SetMonitor(true)
	src.PlaybackErr = os.ErrPermission
	if err := svc.SetMonitorDevice("Headphones"); err == nil {
		t.Fatal("expected error")
	}
	_ = src.Emit(frames(100))
	waitFor(t, "recording continues", func() bool { return svc.Status().SizeBytes == 400 })
	if svc.Status().Monitor {
		t.Fatal("monitor should be off")
	}
	_, _ = svc.Stop()
}

func TestSlowOutputOpenDoesNotStallStatusOrCapture(t *testing.T) {
	svc, src, _, _ := setup(t)
	withOutputs(src)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = svc.SetMonitor(true)
	src.PlayGate = make(chan struct{})
	done := make(chan struct{})
	go func() { _ = svc.SetMonitorDevice("Headphones"); close(done) }()
	time.Sleep(30 * time.Millisecond)
	_ = src.Emit(frames(100))
	waitFor(t, "status and capture while the output opens", func() bool { return svc.Status().SizeBytes == 400 })
	close(src.PlayGate)
	<-done
	if !svc.Status().Monitor {
		t.Fatal("monitor should be on after the slow open")
	}
	_, _ = svc.Stop()
}

func TestCloseInputFinalisesRecording(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(10))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 40 })
	if err := svc.CloseInput(); err != nil {
		t.Fatal(err)
	}
	if svc.Status().State != StateIdle || src.CaptureOpen || len(p.Info().Sides) != 1 {
		t.Fatalf("%+v", svc.Status())
	}
}

func TestOverwriteProtection(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	record := func(side string, overwrite bool, n int) error {
		if err := svc.Start(side, overwrite); err != nil {
			return err
		}
		_ = src.Emit(frames(n))
		want := uint64(n * 4)
		waitFor(t, "data", func() bool { return svc.Status().SizeBytes == want })
		_, err := svc.Stop()
		return err
	}
	if err := record("A", false, 10); err != nil {
		t.Fatal(err)
	}
	var exists *SideExistsError
	if err := svc.Start("A", false); !errors.As(err, &exists) || exists.Side != "A" || err.Error() != "side_exists:A" {
		t.Fatalf("expected side_exists, got %v", err)
	}
	if err := record("B", false, 5); err != nil {
		t.Fatalf("other side must be free: %v", err)
	}
	if err := record("A", true, 20); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav")); fi.Size() != 44+80 {
		t.Fatalf("overwrite size %d", fi.Size())
	}
	if err := svc.Start("C", false); err == nil {
		t.Fatal("unknown side accepted")
	}
}

func TestUnregisteredFileOnDiskCountsAsRecorded(t *testing.T) {
	svc, _, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-B.wav"), []byte("old"), 0o644)
	var exists *SideExistsError
	if err := svc.Start("B", false); !errors.As(err, &exists) {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyTakeKeepsPreviousFile(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(10))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 40 })
	_, _ = svc.Stop()

	if err := svc.Start("A", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(); !errors.Is(err, ErrEmptyTake) {
		t.Fatalf("got %v", err)
	}
	if fi, err := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav")); err != nil || fi.Size() != 44+40 {
		t.Fatalf("old file damaged: %v %v", fi, err)
	}
	if p.HasPart("A") {
		t.Fatal("empty part not removed")
	}
}

func TestDeviceLossFinalisesTakeOverSide(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	_ = src.Emit(frames(10))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 40 })
	_, _ = svc.Stop()

	_ = svc.Start("A", true)
	_ = src.Emit(frames(3))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 12 })
	src.StopCapture()
	waitFor(t, "idle", func() bool { return svc.Status().State == StateIdle })
	// Device loss finalises the take, which then replaces the side.
	if fi, _ := os.Stat(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav")); fi.Size() != 44+12 {
		t.Fatalf("size %d", fi.Size())
	}
}

func TestCrashLeftoverRecovered(t *testing.T) {
	svc, src, p, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("B", false)
	_ = src.Emit(frames(250))
	waitFor(t, "data", func() bool { return svc.Status().SizeBytes == 1000 })
	part, _ := p.PartPath("B")
	// Simulate a crash: copy the part as it is, with its unfinalised header.
	data, _ := os.ReadFile(part)
	_, _ = svc.Stop()
	_ = os.Remove(filepath.Join(p.Dir(), "side-B.wav"))
	_ = p.RemoveSide("B")
	_ = os.WriteFile(part, data[:44+1000-2], 0o644) // torn last frame

	q, _ := project.Open(p.Dir())
	l := q.Info().Leftovers
	if len(l) != 1 || l[0].Side != "B" || l[0].DurationMs != 249 {
		t.Fatalf("%+v", l)
	}
	if err := svc.Start("B", false); err == nil {
		t.Fatal("start with a leftover must be refused")
	}
	if err := svc.SetProject(q); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.RecoverPart("B", false)
	if err != nil || rec.DurationMs != 249 {
		t.Fatalf("%v %+v", err, rec)
	}
	if q.HasPart("B") || !q.SideExists("B") {
		t.Fatal("recovery did not commit")
	}
	if err := svc.Start("B", true); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Stop()
	_ = svc.DiscardPart("B")
}

func TestImportSide(t *testing.T) {
	svc, _, p, _ := setup(t)
	src := filepath.Join("..", "importer", "testdata", "tone16.flac")
	rec, err := svc.ImportSide(context.Background(), "A", src, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Origin != project.OriginImported || rec.SourceName != "tone16.flac" || rec.SampleRate != 44100 || rec.DurationMs < 900 {
		t.Fatalf("%+v", rec)
	}
	if p.Info().Sides["A"].File != "side-A.wav" || p.HasPart("A") || svc.Status().ImportSide != "" {
		t.Fatalf("%+v", p.Info())
	}

	var exists *SideExistsError
	if _, err := svc.ImportSide(context.Background(), "A", src, false, nil); !errors.As(err, &exists) {
		t.Fatalf("expected side_exists, got %v", err)
	}
	// A file that is on disk but not in the descriptor needs the flag too.
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-B.wav"), []byte("old"), 0o644)
	if _, err := svc.ImportSide(context.Background(), "B", src, false, nil); !errors.As(err, &exists) {
		t.Fatalf("expected side_exists for unregistered file, got %v", err)
	}
	if _, err := svc.ImportSide(context.Background(), "B", src, true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFailedOrCancelledImportKeepsOldFile(t *testing.T) {
	svc, _, p, _ := setup(t)
	src := filepath.Join("..", "importer", "testdata", "tone16.flac")
	if _, err := svc.ImportSide(context.Background(), "A", src, false, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav"))

	bad := filepath.Join(t.TempDir(), "bad.mp3")
	_ = os.WriteFile(bad, []byte("not audio at all, really"), 0o644)
	if _, err := svc.ImportSide(context.Background(), "A", bad, true, nil); err == nil {
		t.Fatal("bad file accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ImportSide(ctx, "A", src, true, nil); err == nil {
		t.Fatal("cancel ignored")
	}
	after, _ := os.ReadFile(filepath.Join(p.Dir(), project.StagingDir, "side-A.wav"))
	if !bytes.Equal(before, after) || p.HasPart("A") {
		t.Fatal("previous side file damaged or part left behind")
	}
}

func TestImportAndRecordAreMutuallyExclusive(t *testing.T) {
	svc, _, _, _ := setup(t)
	_ = svc.OpenInput("d1", "USB", stereo16)
	_ = svc.Start("A", false)
	src := filepath.Join("..", "importer", "testdata", "tone16.flac")
	if _, err := svc.ImportSide(context.Background(), "B", src, false, nil); err == nil {
		t.Fatal("import during recording accepted")
	}
	_, _ = svc.Stop()

	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := svc.ImportSide(context.Background(), "B", src, false, func(int64, int64) {
			select {
			case started <- struct{}{}:
				<-release
			default:
			}
		})
		done <- err
	}()
	// Progress is throttled, so fall back to polling the status.
	waitFor(t, "import running or finished", func() bool { return svc.Status().ImportSide == "B" || len(done) == 1 })
	if svc.Status().ImportSide == "B" {
		if err := svc.Start("A", true); err == nil {
			t.Fatal("record during import accepted")
		}
		if !svc.Busy() {
			t.Fatal("Busy must be true")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEmptyTakeErrorFollowsLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale(i18n.English) })
	if !strings.Contains(ErrEmptyTake.Error(), "empty") {
		t.Fatalf("got %q", ErrEmptyTake)
	}
	i18n.SetLocale(i18n.Ukrainian)
	if !strings.Contains(ErrEmptyTake.Error(), "порожній") {
		t.Fatalf("got %q", ErrEmptyTake)
	}
}
