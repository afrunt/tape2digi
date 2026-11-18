// Package recorder owns the recording lifecycle: an open input stream feeds a
// level meter and optional monitor, and a recording writes the same frames to
// a WAV file in the open project.
package recorder

import (
	"context"
	"errors"
	"github.com/afrunt/tape2digi/internal/i18n"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/afrunt/tape2digi/internal/audio"
	"github.com/afrunt/tape2digi/internal/importer"
	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/wav"
)

// State is the recorder lifecycle state.
type State string

const (
	StateIdle      State = "idle"      // no input stream open
	StateMetering  State = "metering"  // stream open, nothing written
	StateRecording State = "recording" // writing to a file
	StatePaused    State = "paused"    // stream open, file kept open, not writing
)

const queueChunks = 4096

// Status is a snapshot for the UI.
type Status struct {
	State     State  `json:"state"`
	Monitor   bool   `json:"monitor"`
	ElapsedMs int64  `json:"elapsedMs"`
	SizeBytes uint64 `json:"sizeBytes"`
	File      string `json:"file"`
	Overruns  uint64 `json:"overruns"`
	Channels  int    `json:"channels"`
	Side      string `json:"side"`
	// ImportSide is the side being imported, empty when no import runs.
	ImportSide string `json:"importSide"`
}

// SideExistsError is returned when a side already has audio and the caller did
// not allow overwriting it. The UI recognises the "side_exists:" prefix.
type SideExistsError struct{ Side string }

func (e *SideExistsError) Error() string { return "side_exists:" + e.Side }

// ErrEmptyTake is returned by Stop when no audio was captured; the previous
// side file, if any, is left untouched.
var ErrEmptyTake = i18n.New("recorder.theRecordingIsEmptyNothing")

// Handlers receive asynchronous notifications. Any may be nil.
type Handlers struct {
	OnLevel func([]audio.ChannelLevel)
	OnState func(Status)
	OnError func(string)
}

// Service is safe for concurrent use.
type Service struct {
	src audio.Source
	h   Handlers

	// LevelInterval throttles OnLevel; zero delivers every chunk.
	LevelInterval time.Duration
	now           func() time.Time

	mu        sync.Mutex
	state     State
	format    audio.Format
	deviceID  string
	deviceNm  string
	stream    audio.Stream
	queue     *audio.Queue
	done      chan struct{}
	monitor   *audio.Monitor
	monWanted bool
	monDevice string // chosen output device name; empty = system default
	// monOp serialises monitor start, stop and output changes.
	monOp sync.Mutex
	proj  *project.Project

	file      *os.File
	writer    *wav.Writer
	recPath   string
	recSide   string
	impSide   string
	startedAt time.Time
	// maxData lowers the WAV size cap in tests.
	maxData uint64
}

// New creates an idle service.
func New(src audio.Source, h Handlers) *Service {
	return &Service{src: src, h: h, state: StateIdle, now: time.Now, LevelInterval: time.Second / 30}
}

// Devices lists capture devices.
func (s *Service) Devices() ([]audio.Device, error) { return s.src.CaptureDevices() }

// SetProject sets the project recordings are saved to; nil clears it.
func (s *Service) SetProject(p *project.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isRecordingLocked() {
		return i18n.New("recorder.theProjectCannotBeChanged")
	}
	if s.impSide != "" {
		return i18n.New("recorder.theProjectCannotBeChanged2")
	}
	s.proj = p
	return nil
}

func (s *Service) isRecordingLocked() bool {
	return s.state == StateRecording || s.state == StatePaused
}

// OpenInput opens the device so meters are live. Any previous input is closed;
// a running recording must be stopped first.
func (s *Service) OpenInput(deviceID, deviceName string, f audio.Format) error {
	s.mu.Lock()
	if s.isRecordingLocked() {
		s.mu.Unlock()
		return i18n.New("recorder.stopRecordingBeforeChangingThe")
	}
	s.closeInputLocked()
	if err := f.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	queue := audio.NewQueue(queueChunks)
	mon := audio.NewMonitor(s.src, f)
	stream, err := s.src.OpenCapture(deviceID, f, func(p []byte) {
		mon.Feed(p)
		queue.Push(p)
	}, func() { s.inputLost(queue) })
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.stream, s.queue, s.monitor = stream, queue, mon
	s.format, s.deviceID, s.deviceNm, s.state = f, deviceID, deviceName, StateMetering
	s.done = make(chan struct{})
	go s.consume(queue, s.done, f)
	wantMonitor := s.monWanted
	s.mu.Unlock()

	if wantMonitor {
		if err := s.SetMonitor(true); err != nil {
			s.notifyError(err.Error())
		}
	}
	s.notifyState()
	return nil
}

// CloseInput closes the input stream, finalising any recording first.
func (s *Service) CloseInput() error {
	s.mu.Lock()
	var err error
	if s.isRecordingLocked() {
		_, err = s.stopLocked()
	}
	s.closeInputLocked()
	s.mu.Unlock()
	s.notifyState()
	return err
}

func (s *Service) closeInputLocked() {
	if s.stream == nil {
		return
	}
	if s.monitor != nil {
		s.monitor.Stop()
	}
	stream, queue, done := s.stream, s.queue, s.done
	s.stream, s.queue, s.monitor, s.done = nil, nil, nil, nil
	s.state = StateIdle
	// Closing the stream must not hold the lock the consumer needs.
	s.mu.Unlock()
	_ = stream.Close()
	queue.Close()
	<-done
	s.mu.Lock()
}

// PlaybackDevices lists output devices.
func (s *Service) PlaybackDevices() ([]audio.Device, error) { return s.src.PlaybackDevices() }

// SetMonitor turns audible monitoring on or off. It never affects recording.
func (s *Service) SetMonitor(on bool) error {
	s.monOp.Lock()
	defer s.monOp.Unlock()
	s.mu.Lock()
	s.monWanted = on
	mon := s.monitor
	s.mu.Unlock()
	if mon == nil {
		if on {
			s.mu.Lock()
			s.monWanted = false
			s.mu.Unlock()
			return i18n.New("recorder.chooseAnInputDeviceFirst")
		}
		return nil
	}
	var err error
	if on {
		err = mon.Start(s.outputID(), func() { s.monitorLost(mon) })
		if err != nil {
			s.mu.Lock()
			s.monWanted = false
			s.mu.Unlock()
		}
	} else {
		mon.Stop()
	}
	s.notifyState()
	return err
}

// SetMonitorDevice chooses the output device by name; empty selects the system
// default. A running monitor moves to the new device. The choice is kept even
// when the device cannot be opened.
func (s *Service) SetMonitorDevice(name string) error {
	s.monOp.Lock()
	defer s.monOp.Unlock()
	s.mu.Lock()
	s.monDevice = name
	mon := s.monitor
	s.mu.Unlock()
	if mon == nil || !mon.Active() {
		return nil
	}
	err := mon.Restart(s.outputID(), func() { s.monitorLost(mon) })
	if err != nil {
		s.mu.Lock()
		s.monWanted = false
		s.mu.Unlock()
	}
	s.notifyState()
	return err
}

// OutputID is the device ID of the chosen monitor output ("" for the default).
func (s *Service) OutputID() string { return s.outputID() }

// outputID resolves the chosen output name to a device ID. An empty, unknown
// or unlistable name resolves to "" (system default).
func (s *Service) outputID() string {
	s.mu.Lock()
	name := s.monDevice
	s.mu.Unlock()
	if name == "" {
		return ""
	}
	devs, err := s.src.PlaybackDevices()
	if err != nil {
		return ""
	}
	for _, d := range devs {
		if d.Name == name {
			return d.ID
		}
	}
	return ""
}

func (s *Service) monitorLost(mon *audio.Monitor) {
	s.mu.Lock()
	current := s.monitor == mon
	if current {
		s.monWanted = false
	}
	s.mu.Unlock()
	if current {
		mon.Stop()
		s.notifyError(i18n.T("recorder.monitorStopped"))
		s.notifyState()
	}
}

// Start begins recording a side of the open project. When the side already has
// audio it fails with *SideExistsError unless overwrite is set. The take is
// written to a temporary file in the project's staging folder; when stopped
// with audio it becomes the side's pending audio, and the project's own side
// file changes only when the project is saved.
func (s *Service) Start(side string, overwrite bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateIdle:
		return i18n.New("recorder.chooseAnInputDeviceFirst")
	case StateRecording, StatePaused:
		return i18n.New("recorder.recordingIsAlreadyInProgress")
	}
	if s.proj == nil {
		return i18n.New("recorder.openOrCreateAProject")
	}
	if s.impSide != "" {
		return i18n.New("recorder.waitForTheImportTo")
	}
	if !project.ValidSide(side) {
		return i18n.New("recorder.unknownSide", side)
	}
	if s.proj.SideExists(side) && !overwrite {
		return &SideExistsError{Side: side}
	}
	if s.proj.HasPart(side) {
		return i18n.New("recorder.sideHasAnUnfinishedRecording", side)
	}
	path, err := s.proj.PartPath(side)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	var opts []wav.Option
	if s.maxData > 0 {
		opts = append(opts, wav.WithMaxDataBytes(s.maxData))
	}
	w, err := wav.NewWriter(f, s.format, opts...)
	if err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	s.file, s.writer, s.recPath, s.recSide, s.startedAt = f, w, path, side, s.now().UTC()
	s.state = StateRecording
	go s.notifyState()
	return nil
}

// Pause stops writing but keeps the stream (and meters) running.
func (s *Service) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRecording {
		return i18n.New("recorder.noRecordingIsInProgress")
	}
	s.state = StatePaused
	go s.notifyState()
	return nil
}

// Resume continues a paused recording in the same file.
func (s *Service) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StatePaused {
		return i18n.New("recorder.theRecordingIsNotPaused")
	}
	s.state = StateRecording
	go s.notifyState()
	return nil
}

// Stop finalises the take and makes it the side's audio. An empty take is
// discarded and returns ErrEmptyTake. If the descriptor update fails the side
// file stays on disk and the error is returned.
func (s *Service) Stop() (project.Side, error) {
	s.mu.Lock()
	rec, err := s.stopLocked()
	s.mu.Unlock()
	s.notifyState()
	return rec, err
}

func (s *Service) stopLocked() (project.Side, error) {
	if !s.isRecordingLocked() {
		return project.Side{}, i18n.New("recorder.noRecordingIsInProgress")
	}
	w, f, part, side, started, format := s.writer, s.file, s.recPath, s.recSide, s.startedAt, s.format
	s.writer, s.file, s.recPath, s.recSide = nil, nil, "", ""
	s.state = StateMetering

	size := w.DataBytes()
	closeErr := w.Close()
	if err := f.Close(); closeErr == nil {
		closeErr = err
	}
	if size == 0 {
		_ = os.Remove(part)
		return project.Side{}, ErrEmptyTake
	}
	rec := project.Side{
		Origin:     project.OriginRecorded,
		RecordedAt: started,
		DurationMs: int64(size * 1000 / uint64(format.SampleRate*format.BlockAlign())),
		SampleRate: format.SampleRate,
		Channels:   format.Channels,
		BitDepth:   format.BitDepth,
		Device:     s.deviceNm,
	}
	if closeErr != nil {
		return rec, i18n.New("recorder.couldNotFinishTheFile", closeErr, filepath.Base(part))
	}
	return rec, s.commitLocked(side, part, rec)
}

// commitLocked keeps a finished temporary take as the side's pending audio; the
// project's side file and descriptor change only on Save.
func (s *Service) commitLocked(side, part string, rec project.Side) error {
	if s.proj == nil {
		return i18n.New("recorder.noProjectIsOpen")
	}
	if err := s.proj.StageSide(side, part, rec); err != nil {
		return i18n.New("recorder.couldNotSaveSide", side, err)
	}
	return nil
}

// RecoverPart turns an unfinished take left by a crash into the side's audio.
func (s *Service) RecoverPart(side string, overwrite bool) (project.Side, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proj == nil {
		return project.Side{}, i18n.New("recorder.noProjectIsOpen")
	}
	if s.isRecordingLocked() {
		return project.Side{}, i18n.New("recorder.stopRecordingBeforeRecovering")
	}
	if !project.ValidSide(side) {
		return project.Side{}, i18n.New("recorder.unknownSide", side)
	}
	if s.proj.SideExists(side) && !overwrite {
		return project.Side{}, &SideExistsError{Side: side}
	}
	part := s.proj.LeftoverPath(side)
	if part == "" {
		return project.Side{}, i18n.New("recorder.thereIsNoUnfinishedRecording")
	}
	f, err := os.OpenFile(part, os.O_RDWR, 0)
	if err != nil {
		return project.Side{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return project.Side{}, err
	}
	format, data, err := wav.Repair(f, st.Size())
	f.Close()
	if err != nil || data == 0 {
		if err == nil {
			err = ErrEmptyTake
		}
		return project.Side{}, i18n.New("recorder.theRecordingCouldNotBe", err)
	}
	rec := project.Side{
		Origin:     project.OriginRecorded,
		RecordedAt: st.ModTime().UTC(),
		DurationMs: int64(data * 1000 / uint64(format.SampleRate*format.BlockAlign())),
		SampleRate: format.SampleRate,
		Channels:   format.Channels,
		BitDepth:   format.BitDepth,
	}
	return rec, s.commitLocked(side, part, rec)
}

// Busy reports whether a take or an import is in progress.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isRecordingLocked() || s.impSide != ""
}

// ImportSide converts an audio file into the side's WAV. It fails with
// *SideExistsError unless overwrite is set, and leaves the previous side file
// untouched on any failure or cancellation.
func (s *Service) ImportSide(ctx context.Context, side, src string, overwrite bool, progress func(done, total int64)) (project.Side, error) {
	s.mu.Lock()
	switch {
	case s.proj == nil:
		s.mu.Unlock()
		return project.Side{}, i18n.New("recorder.noProjectIsOpen")
	case s.isRecordingLocked():
		s.mu.Unlock()
		return project.Side{}, i18n.New("recorder.stopRecordingBeforeImporting")
	case s.impSide != "":
		s.mu.Unlock()
		return project.Side{}, i18n.New("recorder.anImportIsAlreadyIn")
	case !project.ValidSide(side):
		s.mu.Unlock()
		return project.Side{}, i18n.New("recorder.unknownSide", side)
	case s.proj.SideExists(side) && !overwrite:
		s.mu.Unlock()
		return project.Side{}, &SideExistsError{Side: side}
	case s.proj.HasPart(side):
		s.mu.Unlock()
		return project.Side{}, i18n.New("recorder.sideHasAnUnfinishedRecording", side)
	}
	proj := s.proj
	part, _ := proj.PartPath(side)
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		s.mu.Unlock()
		return project.Side{}, err
	}
	s.impSide = side
	s.mu.Unlock()
	go s.notifyState()

	res, err := importer.Convert(ctx, src, part, importer.Options{MaxDataBytes: s.maxData}, progress)

	s.mu.Lock()
	defer func() {
		s.impSide = ""
		s.mu.Unlock()
		s.notifyState()
	}()
	if err != nil {
		return project.Side{}, err
	}
	rec := project.Side{
		Origin:     project.OriginImported,
		RecordedAt: s.now().UTC(),
		DurationMs: res.DurationMs,
		SampleRate: res.Format.SampleRate,
		Channels:   res.Format.Channels,
		BitDepth:   res.Format.BitDepth,
		SourceName: filepath.Base(src),
	}
	return rec, s.commitLocked(side, part, rec)
}

// DiscardPart deletes an unfinished take.
func (s *Service) DiscardPart(side string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proj == nil {
		return i18n.New("recorder.noProjectIsOpen")
	}
	if s.recSide == side && s.isRecordingLocked() {
		return i18n.New("recorder.thisSideIsBeingRecorded")
	}
	return s.proj.RemovePart(side)
}

// Status returns the current snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	st := Status{State: s.state, Channels: s.format.Channels, File: filepath.Base(s.recPath), Side: s.recSide, ImportSide: s.impSide}
	if s.recPath == "" {
		st.File = ""
	}
	if s.monitor != nil {
		st.Monitor = s.monitor.Active()
	}
	if s.queue != nil {
		st.Overruns = s.queue.Overruns()
	}
	if s.writer != nil {
		st.SizeBytes = s.writer.DataBytes()
		if bps := uint64(s.format.SampleRate * s.format.BlockAlign()); bps > 0 {
			st.ElapsedMs = int64(st.SizeBytes * 1000 / bps)
		}
	}
	return st
}

// consume drains the queue: it measures levels and, while recording, writes.
func (s *Service) consume(q *audio.Queue, done chan struct{}, f audio.Format) {
	defer close(done)
	var (
		agg  []audio.ChannelLevel
		last time.Time
	)
	for chunk := range q.Chunks() {
		lv := audio.Levels(chunk, f)
		agg = mergeLevels(agg, lv)
		if t := s.now(); t.Sub(last) >= s.LevelInterval {
			last = t
			if s.h.OnLevel != nil {
				s.h.OnLevel(agg)
			}
			agg = nil
		}
		s.write(chunk)
	}
}

func mergeLevels(agg, lv []audio.ChannelLevel) []audio.ChannelLevel {
	if agg == nil {
		return lv
	}
	for i := range lv {
		if lv[i].PeakDB > agg[i].PeakDB {
			agg[i].PeakDB = lv[i].PeakDB
		}
		agg[i].RMSDB = lv[i].RMSDB
		agg[i].Clip = agg[i].Clip || lv[i].Clip
	}
	return agg
}

func (s *Service) write(chunk []byte) {
	s.mu.Lock()
	if s.state != StateRecording || s.writer == nil {
		s.mu.Unlock()
		return
	}
	_, err := s.writer.Write(chunk)
	if err == nil {
		s.mu.Unlock()
		return
	}
	_, stopErr := s.stopLocked()
	s.mu.Unlock()
	msg := i18n.T("recorder.writeFailed", err)
	if errors.Is(err, wav.ErrSizeLimit) {
		msg = i18n.T("recorder.sizeLimitReached")
	}
	if stopErr != nil && !errors.Is(stopErr, ErrEmptyTake) {
		msg += " " + stopErr.Error()
	}
	s.notifyError(msg)
	s.notifyState()
}

// inputLost handles the capture device disappearing.
func (s *Service) inputLost(q *audio.Queue) {
	go func() {
		s.mu.Lock()
		if s.queue != q {
			s.mu.Unlock()
			return
		}
		var stopErr error
		if s.isRecordingLocked() {
			_, stopErr = s.stopLocked()
		}
		s.closeInputLocked()
		s.mu.Unlock()
		msg := i18n.T("recorder.inputUnavailable")
		if stopErr != nil && !errors.Is(stopErr, ErrEmptyTake) {
			msg += " " + stopErr.Error()
		}
		s.notifyError(msg)
		s.notifyState()
	}()
}

func (s *Service) notifyState() {
	if s.h.OnState != nil {
		s.h.OnState(s.Status())
	}
}

func (s *Service) notifyError(msg string) {
	if s.h.OnError != nil {
		s.h.OnError(msg)
	}
}
