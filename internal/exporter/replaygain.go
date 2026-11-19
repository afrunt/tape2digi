package exporter

import (
	"context"
	"fmt"
	"strings"

	"github.com/afrunt/tape2digi/internal/loudness"
)

// ReplayGainReference is the ReplayGain 2.0 reference loudness in LUFS.
const ReplayGainReference = -18.0

// gainValue is one ReplayGain pair; ok is false for silence.
type gainValue struct {
	Gain, Peak float64
	OK         bool
}

func (g gainValue) gainText() string { return fmt.Sprintf("%.2f dB", g.Gain) }
func (g gainValue) peakText() string { return fmt.Sprintf("%.6f", g.Peak) }

func toGain(r loudness.Result) gainValue {
	l, ok := r.LUFS()
	if !ok {
		return gainValue{}
	}
	return gainValue{Gain: ReplayGainReference - l, Peak: r.Peak, OK: ok}
}

// replayGain holds per-track values (same order as the tracks) and the album value.
type replayGain struct {
	Tracks []gainValue
	Album  gainValue
}

// track returns the value of track i, or nothing when ReplayGain is off.
func (r *replayGain) track(i int) gainValue {
	if r == nil || i >= len(r.Tracks) {
		return gainValue{}
	}
	return r.Tracks[i]
}

func (r *replayGain) album() gainValue {
	if r == nil {
		return gainValue{}
	}
	return r.Album
}

// measureReplayGain measures every track of the source audio.
func measureReplayGain(ctx context.Context, tracks []Track, pr *progress) (*replayGain, error) {
	out := &replayGain{}
	all := make([]loudness.Result, 0, len(tracks))
	for _, t := range tracks {
		r, closeFn, err := openPCM(ctx, t)
		if err != nil {
			return nil, err
		}
		var last int64
		res, err := loudness.MeasurePCM(ctx, r, t.Side.Format, func(n int64) { pr.add(n - last); last = n })
		closeFn()
		if err != nil {
			return nil, err
		}
		all = append(all, res)
		out.Tracks = append(out.Tracks, toGain(res))
	}
	out.Album = toGain(loudness.Combine(all...))
	return out, nil
}

// replayGainTags returns the ReplayGain tag names and values; album only
// when track is the zero value.
func replayGainTags(track, album gainValue) [][2]string {
	var tags [][2]string
	if track.OK {
		tags = append(tags, [2]string{"REPLAYGAIN_TRACK_GAIN", track.gainText()}, [2]string{"REPLAYGAIN_TRACK_PEAK", track.peakText()})
	}
	if album.OK {
		tags = append(tags, [2]string{"REPLAYGAIN_ALBUM_GAIN", album.gainText()}, [2]string{"REPLAYGAIN_ALBUM_PEAK", album.peakText()})
	}
	return tags
}

func cueReplayGain(b *strings.Builder, indent string, tags [][2]string) {
	for _, kv := range tags {
		fmt.Fprintf(b, "%sREM %s %s\n", indent, kv[0], kv[1])
	}
}
