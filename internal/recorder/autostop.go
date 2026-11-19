package recorder

import (
	"math"

	"github.com/afrunt/tape2digi/internal/audio"
)

// Auto-stop tuning: levels are judged on 50 ms windows; the take must be at
// least armMs long before it can stop, and keepMs of the silence stays.
const (
	watchWinMs   = 50
	armMs        = 30_000
	keepMs       = 2_000
	digitalDB    = -90.0
	fixedDB      = -55.0
	minSpreadDB  = 20.0
	binsPerDB    = 2
	histBins     = int(-digitalDB) * binsPerDB
	minAdaptWins = armMs / watchWinMs
)

// silenceWatch follows the level of a take and reports when the end-of-tape
// silence has lasted long enough. It is used only from the consumer goroutine
// under the service lock.
type silenceWatch struct {
	f         audio.Format
	winFrames int
	sum       float64
	frames    int
	hist      [histBins]int
	counted   int
	runWins   int
	runStart  uint64 // data offset of the first silent window
	winStart  uint64 // data offset of the window being filled
}

func newSilenceWatch(f audio.Format) *silenceWatch {
	return &silenceWatch{f: f, winFrames: max(1, f.SampleRate*watchWinMs/1000)}
}

// reset forgets the current silent stretch and the partial window (after a
// pause the audio is not continuous). The level statistics are kept.
func (w *silenceWatch) reset(offset uint64) {
	w.sum, w.frames, w.runWins = 0, 0, 0
	w.winStart = offset
}

// threshold is the level below which a window counts as silence.
func (w *silenceWatch) threshold() float64 {
	if w.counted < minAdaptWins {
		return fixedDB
	}
	floor, music := w.percentile(0.01), w.percentile(0.5)
	if music-floor < minSpreadDB {
		return fixedDB
	}
	return floor + 0.25*(music-floor)
}

func (w *silenceWatch) percentile(p float64) float64 {
	target := int(math.Ceil(p * float64(w.counted)))
	n := 0
	for i, c := range w.hist {
		n += c
		if n >= max(target, 1) {
			return digitalDB + float64(i)/binsPerDB
		}
	}
	return 0
}

// feed processes a chunk written at data offset off and returns the data
// offset to keep when the silence has lasted limitMs; otherwise 0.
func (w *silenceWatch) feed(chunk []byte, off uint64, limitMs int64) uint64 {
	bps := w.f.BitDepth / 8
	frame := bps * w.f.Channels
	if frame == 0 {
		return 0
	}
	full := float64(int64(1) << (w.f.BitDepth - 1))
	bytesPerSec := uint64(w.f.SampleRate * frame)
	n := len(chunk) / frame
	for i := 0; i < n; i++ {
		var mix float64
		for c := 0; c < w.f.Channels; c++ {
			o := i*frame + c*bps
			var s int32
			if bps == 2 {
				s = int32(int16(uint16(chunk[o]) | uint16(chunk[o+1])<<8))
			} else {
				s = int32(chunk[o])<<8 | int32(chunk[o+1])<<16 | int32(int8(chunk[o+2]))<<24
				s >>= 8
			}
			mix += float64(s)
		}
		mix /= float64(w.f.Channels) * full
		w.sum += mix * mix
		w.frames++
		if w.frames < w.winFrames {
			continue
		}
		db := 10 * math.Log10(w.sum/float64(w.frames)+1e-20)
		start := w.winStart
		w.winStart = off + uint64((i+1)*frame)
		w.sum, w.frames = 0, 0
		if db > digitalDB {
			w.hist[min(histBins-1, int((db-digitalDB)*binsPerDB))]++
			w.counted++
		}
		if db > digitalDB && db >= w.threshold() {
			w.runWins = 0
			continue
		}
		if w.runWins == 0 {
			w.runStart = start
		}
		w.runWins++
		end := w.winStart
		if int64(w.runWins*watchWinMs) >= limitMs && end*1000 >= armMs*bytesPerSec {
			return w.runStart + keepMs*bytesPerSec/1000
		}
	}
	return 0
}
