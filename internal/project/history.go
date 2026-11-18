package project

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/afrunt/tape2digi/internal/i18n"
)

// maxHistorySteps is how many editor changes a side remembers.
const maxHistorySteps = 100

// History tells whether a side has changes to undo or redo.
type History struct {
	CanUndo bool `json:"canUndo"`
	CanRedo bool `json:"canRedo"`
}

// DetailsKey is the history entry of project-wide changes (the Compilation
// flag); it is not a side.
const DetailsKey = "project"

// step is one undoable change: a marker edit, an audio edit or a change of the
// Compilation flag.
type step struct {
	audio bool
	// Compilation step: the flag before and after.
	details               bool
	compBefore, compAfter bool
	// Marker step: the draft before and after; nil means the side had no draft.
	before, after *[]Marker
	// Audio step: whether the side already had a staged file before the edit,
	// the side record to stage again on redo, and the version file that holds
	// the audio which is not live right now ("" when there is none).
	hadStaged bool
	side      Side
	stored    string
}

type sideHistory struct {
	steps []step
	pos   int   // steps[:pos] are applied, steps[pos:] can be redone
	n     int   // counter for version file names
	touch int64 // project-wide order of the last change, undo or redo
}

func copyMarkers(list []Marker) *[]Marker {
	c := append([]Marker{}, list...)
	return &c
}

func (p *Project) touchLocked(h *sideHistory) {
	p.historyTouch++
	h.touch = p.historyTouch
}

func (p *Project) historyLocked(key string) *sideHistory {
	if p.history == nil {
		p.history = map[string]*sideHistory{}
	}
	h := p.history[key]
	if h == nil {
		h = &sideHistory{}
		p.history[key] = h
	}
	return h
}

func (p *Project) versionPathLocked(key string, h *sideHistory) (string, error) {
	h.n++
	return p.stagingPath(key, fmt.Sprintf(".v%d.wav", h.n))
}

// pushLocked records a new change and drops the steps that could be redone.
func (p *Project) pushLocked(key string, s step) {
	h := p.historyLocked(key)
	for _, old := range h.steps[h.pos:] {
		removeStored(old)
	}
	h.steps = append(h.steps[:h.pos], s)
	for len(h.steps) > maxHistorySteps {
		removeStored(h.steps[0])
		h.steps = h.steps[1:]
	}
	h.pos = len(h.steps)
	p.touchLocked(h)
}

func removeStored(s step) {
	if s.stored != "" {
		_ = os.Remove(s.stored)
	}
}

// clearHistoryLocked forgets a side's steps and deletes their version files.
func (p *Project) clearHistoryLocked(key string) {
	if h := p.history[key]; h != nil {
		for _, s := range h.steps {
			removeStored(s)
		}
	}
	delete(p.history, key)
}

func (p *Project) clearAllHistoryLocked() {
	for k := range p.history {
		p.clearHistoryLocked(k)
	}
	p.history = nil
}

// removeStaleVersions deletes version files left behind by an earlier session.
func removeStaleVersions(dir string) {
	old, _ := filepath.Glob(filepath.Join(dir, StagingDir, "side-*.v*.wav"))
	for _, f := range old {
		_ = os.Remove(f)
	}
}

// History reports what the side can undo and redo.
func (p *Project) History(key string) History {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.history[key]
	if h == nil {
		return History{}
	}
	return History{CanUndo: h.pos > 0, CanRedo: h.pos < len(h.steps)}
}

// ApplyEdit makes tmp, written by an audio edit, the side's staged audio and
// records the change so it can be undone. The side's own file is untouched.
func (p *Project) ApplyEdit(key, tmp string) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	dst, err := p.stagedPath(key)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.historyLocked(key)
	_, had := p.staged[key]
	st := step{audio: true, hadStaged: had}
	if had {
		if st.stored, err = p.versionPathLocked(key, h); err != nil {
			return err
		}
		if err := os.Rename(dst, st.stored); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if had {
			_ = os.Rename(st.stored, dst)
		}
		return err
	}
	if !had {
		s := p.sides[key]
		s.Staged, s.Missing = true, false
		if p.staged == nil {
			p.staged = map[string]Side{}
		}
		p.staged[key] = s
	}
	st.side = p.staged[key]
	p.pushLocked(key, st)
	return nil
}

// Undo reverts the side's last change; audio reports whether the audio changed.
func (p *Project) Undo(key string) (audio bool, err error) {
	if !ValidSide(key) && key != DetailsKey {
		return false, i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.history[key]
	if h == nil || h.pos == 0 {
		return false, i18n.New("project.nothingToUndo")
	}
	s := &h.steps[h.pos-1]
	switch {
	case s.details:
		p.restoreCompilationLocked(s.compBefore)
	case s.audio:
		err = p.swapAudioLocked(key, h, s, true)
	default:
		p.restoreDraftLocked(key, s.before)
	}
	if err != nil {
		return false, err
	}
	h.pos--
	p.touchLocked(h)
	return s.audio, nil
}

// Redo applies the change that was last undone.
func (p *Project) Redo(key string) (audio bool, err error) {
	if !ValidSide(key) && key != DetailsKey {
		return false, i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.history[key]
	if h == nil || h.pos >= len(h.steps) {
		return false, i18n.New("project.nothingToRedo")
	}
	s := &h.steps[h.pos]
	switch {
	case s.details:
		p.restoreCompilationLocked(s.compAfter)
	case s.audio:
		err = p.swapAudioLocked(key, h, s, false)
	default:
		p.restoreDraftLocked(key, s.after)
	}
	if err != nil {
		return false, err
	}
	h.pos++
	p.touchLocked(h)
	return s.audio, nil
}

// restoreCompilationLocked sets only the Compilation flag on top of the
// current staged details; the draft is dropped when nothing differs from the saved details.
func (p *Project) restoreCompilationLocked(on bool) {
	d := p.effectiveDetailsLocked()
	d.Compilation = on
	if d == p.details {
		p.draftDetails = nil
		return
	}
	p.draftDetails = &d
}

func (p *Project) restoreDraftLocked(key string, d *[]Marker) {
	if d == nil {
		delete(p.draftMarkers, key)
		return
	}
	if p.draftMarkers == nil {
		p.draftMarkers = map[string][]Marker{}
	}
	p.draftMarkers[key] = append([]Marker{}, *d...)
}

// swapAudioLocked exchanges the live staged file with the stored version.
// undo moves the side back to its state before the edit, redo forward again.
func (p *Project) swapAudioLocked(key string, h *sideHistory, s *step, undo bool) error {
	live, err := p.stagedPath(key)
	if err != nil {
		return err
	}
	other, err := p.versionPathLocked(key, h)
	if err != nil {
		return err
	}
	// The file that is live moves out of the way unless it does not exist
	// (a side that was not staged before the edit has no live file).
	_, staged := p.staged[key]
	if staged {
		if err := os.Rename(live, other); err != nil {
			return err
		}
	} else {
		other = ""
	}
	if s.stored != "" {
		if err := os.Rename(s.stored, live); err != nil {
			if other != "" {
				_ = os.Rename(other, live)
			}
			return err
		}
	}
	switch {
	case undo && !s.hadStaged:
		delete(p.staged, key)
	case !undo && !staged:
		if p.staged == nil {
			p.staged = map[string]Side{}
		}
		p.staged[key] = s.side
	}
	s.stored = other
	return nil
}

// LastHistory tells whether any side can undo or redo and which side each
// would act on: the most recently touched one.
type LastHistory struct {
	CanUndo  bool   `json:"canUndo"`
	CanRedo  bool   `json:"canRedo"`
	UndoSide string `json:"undoSide"`
	RedoSide string `json:"redoSide"`
}

// LastHistory reports the side an undo or redo from outside the editor acts on.
func (p *Project) LastHistory() LastHistory {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out LastHistory
	var undoAt, redoAt int64
	for key, h := range p.history {
		if h.pos > 0 && h.touch > undoAt {
			undoAt, out.UndoSide, out.CanUndo = h.touch, key, true
		}
		if h.pos < len(h.steps) && h.touch > redoAt {
			redoAt, out.RedoSide, out.CanRedo = h.touch, key, true
		}
	}
	return out
}
