package audio

import "math"

// ChannelLevel is the level of one channel for a chunk of audio.
type ChannelLevel struct {
	PeakDB float64 `json:"peakDb"`
	RMSDB  float64 `json:"rmsDb"`
	Clip   bool    `json:"clip"`
}

// SilenceDB is reported for digital silence.
const SilenceDB = -120.0

// Levels computes per-channel peak/RMS in dBFS from interleaved little-endian
// PCM. It returns one entry per channel.
func Levels(pcm []byte, f Format) []ChannelLevel {
	bps := f.BitDepth / 8
	frame := bps * f.Channels
	out := make([]ChannelLevel, f.Channels)
	if frame == 0 || len(pcm) < frame {
		for i := range out {
			out[i] = ChannelLevel{PeakDB: SilenceDB, RMSDB: SilenceDB}
		}
		return out
	}
	full := float64(int64(1) << (f.BitDepth - 1))
	frames := len(pcm) / frame
	peak := make([]float64, f.Channels)
	sum := make([]float64, f.Channels)
	clip := make([]bool, f.Channels)
	for i := 0; i < frames; i++ {
		for c := 0; c < f.Channels; c++ {
			o := i*frame + c*bps
			var s int32
			if bps == 2 {
				s = int32(int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8))
			} else {
				s = int32(pcm[o])<<8 | int32(pcm[o+1])<<16 | int32(int8(pcm[o+2]))<<24
				s >>= 8
			}
			v := math.Abs(float64(s))
			if v > peak[c] {
				peak[c] = v
			}
			sum[c] += v * v
			if v >= full-1 {
				clip[c] = true
			}
		}
	}
	for c := range out {
		out[c] = ChannelLevel{
			PeakDB: toDB(peak[c] / full),
			RMSDB:  toDB(math.Sqrt(sum[c]/float64(frames)) / full),
			Clip:   clip[c],
		}
	}
	return out
}

func toDB(x float64) float64 {
	if x <= 0 {
		return SilenceDB
	}
	if db := 20 * math.Log10(x); db > SilenceDB {
		return db
	}
	return SilenceDB
}
