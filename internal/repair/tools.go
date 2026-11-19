package repair

import (
	"errors"
	"math"

	"github.com/afrunt/tape2digi/internal/wav"
)

var (
	errStereoOnly = errors.New("repair: the tool needs a stereo recording")
	errUnknown    = errors.New("repair: unknown tool or option")
)

type emitFn func(y []float64) error

// processor turns input frames into output frames; end flushes what is held back.
type processor struct {
	frame func(x []float64, emit emitFn) error
	end   func(emit emitFn) error
}

func noEnd(emitFn) error { return nil }

func tool(op Op, f wav.Format) (processor, error) {
	switch op.Tool {
	case ToolDC:
		return dcTool(op, f), nil
	case ToolHum:
		if op.HumHz != 50 && op.HumHz != 60 {
			return processor{}, errUnknown
		}
		return humTool(op, f), nil
	case ToolBalance:
		if math.IsNaN(op.BalanceDB) || math.Abs(op.BalanceDB) > MaxBalanceDB {
			return processor{}, errUnknown
		}
		return balanceTool(op), nil
	case ToolAzimuth:
		if op.Delay == 0 || math.Abs(float64(op.Delay)) > float64(f.SampleRate)*azimuthMaxMs/1000 {
			return processor{}, errUnknown
		}
		return azimuthTool(op), nil
	case ToolMono:
		return monoTool(op)
	}
	return processor{}, errUnknown
}

// dcTool is a first-order high-pass, primed with the offset so it starts settled.
func dcTool(op Op, f wav.Format) processor {
	rc := 1 / (2 * math.Pi * dcCutHz)
	a := rc / (rc + 1/float64(f.SampleRate))
	px := make([]float64, f.Channels)
	py := make([]float64, f.Channels)
	for c := range px {
		if c < len(op.DC) {
			px[c] = op.DC[c]
		}
	}
	y := make([]float64, f.Channels)
	return processor{frame: func(x []float64, emit emitFn) error {
		for c, v := range x {
			py[c] = a * (py[c] + v - px[c])
			px[c] = v
			y[c] = py[c]
		}
		return emit(y)
	}, end: noEnd}
}

func humTool(op Op, f wav.Format) processor {
	filters := make([][]biquad, f.Channels)
	for c := range filters {
		for k := 1; k <= humHarmonics; k++ {
			h := float64(op.HumHz * k)
			if h >= float64(f.SampleRate)/2 {
				break
			}
			filters[c] = append(filters[c], notch(f.SampleRate, h, humQ))
		}
	}
	y := make([]float64, f.Channels)
	return processor{frame: func(x []float64, emit emitFn) error {
		for c, v := range x {
			for i := range filters[c] {
				v = filters[c][i].step(v)
			}
			y[c] = v
		}
		return emit(y)
	}, end: noEnd}
}

func balanceTool(op Op) processor {
	c, db := BalanceGain(op.BalanceDB, op.PeakL, op.PeakR)
	g := math.Pow(10, db/20)
	y := make([]float64, 2)
	return processor{frame: func(x []float64, emit emitFn) error {
		copy(y, x)
		y[c] *= g
		return emit(y)
	}, end: noEnd}
}

// azimuthTool moves the later channel earlier by |Delay| samples; its last
// samples become silence so the length stays the same.
func azimuthTool(op Op) processor {
	late, early := 1, 0
	d := op.Delay
	if d < 0 {
		late, early, d = 0, 1, -d
	}
	held := make([]float64, d) // early-channel samples waiting for their partner
	var head, count int
	y := make([]float64, 2)
	return processor{frame: func(x []float64, emit emitFn) error {
		if count < d {
			held[(head+count)%d] = x[early]
			count++
			return nil
		}
		y[early], y[late] = held[head], x[late]
		held[head] = x[early]
		head = (head + 1) % d
		return emit(y)
	}, end: func(emit emitFn) error {
		for ; count > 0; count-- {
			y[early], y[late] = held[head], 0
			head = (head + 1) % d
			if err := emit(y); err != nil {
				return err
			}
		}
		return nil
	}}
}

func monoTool(op Op) (processor, error) {
	var pick func(x []float64) float64
	switch op.Mono {
	case MonoLeft:
		pick = func(x []float64) float64 { return x[0] }
	case MonoRight:
		pick = func(x []float64) float64 { return x[1] }
	case MonoMix:
		pick = func(x []float64) float64 { return (x[0] + x[1]) / 2 }
	default:
		return processor{}, errUnknown
	}
	y := make([]float64, 2)
	return processor{frame: func(x []float64, emit emitFn) error {
		v := pick(x)
		y[0], y[1] = v, v
		return emit(y)
	}, end: noEnd}, nil
}
