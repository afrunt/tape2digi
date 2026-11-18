package wav

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

const copyBlock = 1 << 20

// ErrNotEditable is returned for files that are not 16 or 24-bit PCM with one or two channels.
// ErrEmptyRange is returned when a peak range holds no frames.
var ErrEmptyRange = errors.New("wav: empty range")

var ErrNotEditable = errors.New("wav: not a 16 or 24-bit PCM file")

// ReadHeader walks the RIFF chunks and returns the format and the position
// and length of the audio data. The length is bounded by the file size and
// whole frames; a zero or oversized header length (an interrupted take) means
// "to the end of the file".
func ReadHeader(r io.ReaderAt, size int64) (Format, int64, int64, error) {
	var riff [12]byte
	if _, err := r.ReadAt(riff[:], 0); err != nil || string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return Format{}, 0, 0, ErrNotEditable
	}
	var format Format
	haveFmt := false
	off := int64(12)
	for off+8 <= size {
		var h [8]byte
		if _, err := r.ReadAt(h[:], off); err != nil {
			return Format{}, 0, 0, ErrNotEditable
		}
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		body := off + 8
		switch string(h[0:4]) {
		case "fmt ":
			if n < 16 {
				return Format{}, 0, 0, ErrNotEditable
			}
			b := make([]byte, 16)
			if _, err := r.ReadAt(b, body); err != nil {
				return Format{}, 0, 0, ErrNotEditable
			}
			tag := binary.LittleEndian.Uint16(b)
			if tag == 0xFFFE && n >= 26 {
				var sub [2]byte
				if _, err := r.ReadAt(sub[:], body+24); err == nil {
					tag = binary.LittleEndian.Uint16(sub[:])
				}
			}
			if tag != 1 {
				return Format{}, 0, 0, ErrNotEditable
			}
			format = Format{
				Channels:   int(binary.LittleEndian.Uint16(b[2:])),
				SampleRate: int(binary.LittleEndian.Uint32(b[4:])),
				BitDepth:   int(binary.LittleEndian.Uint16(b[14:])),
			}
			if format.Validate() != nil {
				return Format{}, 0, 0, ErrNotEditable
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Format{}, 0, 0, ErrNotEditable
			}
			length := size - body
			if n != 0 && n < length {
				length = n
			}
			if length < 0 {
				length = 0
			}
			length -= length % int64(format.BlockAlign())
			return format, body, length, nil
		}
		off = body + n + n%2
	}
	return Format{}, 0, 0, ErrNotEditable
}

// PeakBuckets is the resolution of a peak table.
const PeakBuckets = 4096

// Peaks summarises a file: per channel and bucket the smallest and largest
// sample (-1..1), plus the largest positive sample and negative magnitude as
// integers for gain maths.
type Peaks struct {
	Format  Format
	Frames  int64
	MaxPos  int64
	MaxNeg  int64
	Buckets int
	Min     [][]float32
	Max     [][]float32
}

// Analyze reads the whole file once and builds its peak table.
func Analyze(ctx context.Context, path string) (*Peaks, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	format, off, length, err := ReadHeader(f, st.Size())
	if err != nil {
		return nil, err
	}
	frame := int64(format.BlockAlign())
	frames := length / frame
	buckets := PeakBuckets
	if int64(buckets) > frames {
		buckets = int(frames)
	}
	p := &Peaks{Format: format, Frames: frames, Buckets: buckets}
	p.Min = make([][]float32, format.Channels)
	p.Max = make([][]float32, format.Channels)
	for c := range p.Min {
		p.Min[c] = make([]float32, buckets)
		p.Max[c] = make([]float32, buckets)
	}
	if frames == 0 {
		return p, nil
	}
	bps := format.BitDepth / 8
	scale := float32(int64(1) << (format.BitDepth - 1))
	buf := make([]byte, copyBlock-copyBlock%int(frame))
	var done int64
	for done < frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := int64(len(buf)) / frame
		if n > frames-done {
			n = frames - done
		}
		chunk := buf[:n*frame]
		if _, err := f.ReadAt(chunk, off+done*frame); err != nil {
			return nil, err
		}
		for i := int64(0); i < n; i++ {
			b := int((done + i) * int64(buckets) / frames)
			for c := 0; c < format.Channels; c++ {
				v := sampleAt(chunk[int(i)*int(frame)+c*bps:], bps)
				x := float32(v) / scale
				first := done+i == 0 || b != int((done+i-1)*int64(buckets)/frames)
				if first || x < p.Min[c][b] {
					p.Min[c][b] = x
				}
				if first || x > p.Max[c][b] {
					p.Max[c][b] = x
				}
				if v > p.MaxPos {
					p.MaxPos = v
				}
				if -v > p.MaxNeg {
					p.MaxNeg = -v
				}
			}
		}
		done += n
	}
	return p, nil
}

// RangePeaks scans only frames [from, to) of the file and returns per channel
// and bucket the smallest and largest sample (-1..1). The range is clamped to
// the audio; buckets never exceed the number of frames in it.
func RangePeaks(ctx context.Context, path string, from, to int64, buckets int) (min, max [][]float32, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	format, off, length, err := ReadHeader(f, st.Size())
	if err != nil {
		return nil, nil, err
	}
	frame := int64(format.BlockAlign())
	total := length / frame
	from, to = max64(from, 0), min64(to, total)
	if to <= from || buckets <= 0 {
		return nil, nil, ErrEmptyRange
	}
	frames := to - from
	if int64(buckets) > frames {
		buckets = int(frames)
	}
	min, max = make([][]float32, format.Channels), make([][]float32, format.Channels)
	for c := range min {
		min[c], max[c] = make([]float32, buckets), make([]float32, buckets)
	}
	bps := format.BitDepth / 8
	scale := float32(int64(1) << (format.BitDepth - 1))
	buf := make([]byte, copyBlock-copyBlock%int(frame))
	var done int64
	for done < frames {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		n := min64(int64(len(buf))/frame, frames-done)
		chunk := buf[:n*frame]
		if _, err := f.ReadAt(chunk, off+(from+done)*frame); err != nil {
			return nil, nil, err
		}
		for i := int64(0); i < n; i++ {
			b := int((done + i) * int64(buckets) / frames)
			first := done+i == 0 || b != int((done+i-1)*int64(buckets)/frames)
			for c := 0; c < format.Channels; c++ {
				x := float32(sampleAt(chunk[int(i*frame)+c*bps:], bps)) / scale
				if first || x < min[c][b] {
					min[c][b] = x
				}
				if first || x > max[c][b] {
					max[c][b] = x
				}
			}
		}
		done += n
	}
	return min, max, nil
}

// Envelope returns the RMS level of every windowMs-long stretch of the file,
// over all channels, in dBFS (silence is -120). The last window may be shorter.
func Envelope(ctx context.Context, path string, windowMs int) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	format, off, length, err := ReadHeader(f, st.Size())
	if err != nil {
		return nil, err
	}
	frame := int64(format.BlockAlign())
	frames := length / frame
	win := int64(format.SampleRate) * int64(windowMs) / 1000
	if win < 1 {
		win = 1
	}
	bps := format.BitDepth / 8
	scale := float64(int64(1) << (format.BitDepth - 1))
	out := make([]float32, 0, frames/win+1)
	buf := make([]byte, copyBlock-copyBlock%int(frame))
	var sum float64
	var inWin int64
	flush := func() {
		if inWin == 0 {
			return
		}
		ms := sum / float64(inWin*int64(format.Channels)) / (scale * scale)
		db := -120.0
		if ms > 1e-12 {
			db = 10 * math.Log10(ms)
		}
		out = append(out, float32(db))
		sum, inWin = 0, 0
	}
	for done := int64(0); done < frames; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := int64(len(buf)) / frame
		if n > frames-done {
			n = frames - done
		}
		chunk := buf[:n*frame]
		if _, err := f.ReadAt(chunk, off+done*frame); err != nil {
			return nil, err
		}
		for i := int64(0); i < n; i++ {
			for c := 0; c < format.Channels; c++ {
				v := float64(sampleAt(chunk[int(i)*int(frame)+c*bps:], bps))
				sum += v * v
			}
			if inWin++; inWin == win {
				flush()
			}
		}
		done += n
	}
	flush()
	return out, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func sampleAt(b []byte, bps int) int64 {
	if bps == 2 {
		return int64(int16(binary.LittleEndian.Uint16(b)))
	}
	v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
	return int64(v << 8 >> 8)
}

func putSample(b []byte, bps int, v int64) {
	b[0], b[1] = byte(v), byte(v>>8)
	if bps == 3 {
		b[2] = byte(v >> 16)
	}
}

// Gain is a rational amplification factor Num/Den.
type Gain struct{ Num, Den int64 }

// DB returns the gain in decibels.
func (g Gain) DB() float64 { return 20 * math.Log10(float64(g.Num)/float64(g.Den)) }

func scaleRound(x int64, g Gain) int64 {
	neg := x < 0
	if neg {
		x = -x
	}
	r := (2*x*g.Num + g.Den) / (2 * g.Den)
	if neg {
		return -r
	}
	return r
}

// MaxGain returns the largest factor that keeps every sample inside the
// range of the bit depth, the same for all channels. ok is false when the
// audio is silent, already at full scale, or no sample would change.
func MaxGain(maxPos, maxNeg int64, bitDepth int) (Gain, bool) {
	limit := int64(1)<<(bitDepth-1) - 1
	var g Gain
	have := false
	if maxPos > 0 {
		g, have = Gain{limit, maxPos}, true
	}
	if maxNeg > 0 {
		n := Gain{limit + 1, maxNeg}
		if !have || n.Num*g.Den < g.Num*n.Den {
			g = n
		}
		have = true
	}
	if !have || g.Num <= g.Den {
		return Gain{}, false
	}
	if scaleRound(maxPos, g) == maxPos && scaleRound(-maxNeg, g) == -maxNeg {
		return Gain{}, false
	}
	return g, true
}

// Amplify copies src to dst applying the gain to every sample with rounding.
// The caller owns dst; on error or cancellation it is removed.
func Amplify(ctx context.Context, src, dst string, g Gain, progress func(done, total int64)) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	format, off, length, err := ReadHeader(in, st.Size())
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	w, err := NewWriter(out, format)
	if err != nil {
		return err
	}
	bps := format.BitDepth / 8
	limit := int64(1)<<(format.BitDepth-1) - 1
	buf := make([]byte, copyBlock-copyBlock%format.BlockAlign())
	var done int64
	for done < length {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := int64(len(buf))
		if n > length-done {
			n = length - done
		}
		chunk := buf[:n]
		if _, err = in.ReadAt(chunk, off+done); err != nil {
			return err
		}
		for i := 0; i+bps <= len(chunk); i += bps {
			v := scaleRound(sampleAt(chunk[i:], bps), g)
			if v > limit {
				v = limit
			} else if v < -limit-1 {
				v = -limit - 1
			}
			putSample(chunk[i:], bps, v)
		}
		if _, err = w.Write(chunk); err != nil {
			return err
		}
		done += n
		if progress != nil {
			progress(done, length)
		}
	}
	if err = w.Close(); err != nil {
		return err
	}
	return out.Close()
}
