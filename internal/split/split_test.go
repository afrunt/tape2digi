package split

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/afrunt/tape2digi/internal/project"
	"github.com/afrunt/tape2digi/internal/wav"
)

// build makes an envelope of consecutive segments of (seconds, dB).
func build(segs ...[2]float64) []float32 {
	var env []float32
	for _, s := range segs {
		for i := 0; i < int(s[0]*1000/WindowMs); i++ {
			env = append(env, float32(s[1]))
		}
	}
	return env
}

func TestDetectFindsTracksBetweenGaps(t *testing.T) {
	env := build(
		[2]float64{12, -70}, [2]float64{100, -30}, [2]float64{5, -70}, [2]float64{80, -28},
		[2]float64{1, -70}, [2]float64{20, -30}, // 1 s dip is not a gap
		[2]float64{8, -69}, [2]float64{60, -30}, [2]float64{10, -70},
	)
	got := Detect(env)
	want := []project.Marker{
		{Kind: "start", PositionMs: 11750}, {Kind: "end", PositionMs: 112250},
		{Kind: "start", PositionMs: 116750}, {Kind: "end", PositionMs: 218250},
		{Kind: "start", PositionMs: 225750}, {Kind: "end", PositionMs: 286250},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marker %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestDetectUnavailableWithoutClearTracks(t *testing.T) {
	if Detect(build([2]float64{120, -30})) != nil {
		t.Fatal("continuous signal must not split")
	}
	if Detect(build([2]float64{10, -70}, [2]float64{120, -30}, [2]float64{10, -70})) != nil {
		t.Fatal("a single track must not count as a split")
	}
	if Detect(build([2]float64{60, -40}, [2]float64{5, -50}, [2]float64{60, -40})) != nil {
		t.Fatal("low spread must not split")
	}
}

func tracks(t *testing.T, env []float32) int {
	t.Helper()
	return len(Detect(env)) / 2
}

func TestDetectShortPausesAreSmallShareOfSide(t *testing.T) {
	env := build(
		[2]float64{1, -68}, [2]float64{100, -25}, [2]float64{2.5, -68}, [2]float64{100, -25},
		[2]float64{2.5, -68}, [2]float64{100, -25}, [2]float64{1, -68},
	)
	if got := tracks(t, env); got != 3 {
		t.Fatalf("got %d tracks, want 3", got)
	}
}

func TestDetectFadeOutEndingInBriefDip(t *testing.T) {
	fadeGap := [][2]float64{{1.5, -45}, {1, -68}, {0.5, -45}}
	segs := [][2]float64{{12, -68}, {100, -25}}
	segs = append(segs, fadeGap...)
	segs = append(segs, [2]float64{100, -25})
	segs = append(segs, fadeGap...)
	segs = append(segs, [2]float64{100, -25}, [2]float64{10, -68})
	if got := tracks(t, build(segs...)); got != 3 {
		t.Fatalf("got %d tracks, want 3", got)
	}
}

func TestDetectQuietPassageIsNotAGap(t *testing.T) {
	env := build(
		[2]float64{12, -68}, [2]float64{60, -25},
		[2]float64{2, -50}, [2]float64{0.3, -68}, [2]float64{2, -50}, // a quiet bar with a brief drop
		[2]float64{60, -25}, [2]float64{10, -68},
	)
	if got := tracks(t, env); got != 0 {
		t.Fatalf("got %d tracks, want no split", got)
	}
}

func TestDetectFadeBetweenSongsIsOneCut(t *testing.T) {
	env := build(
		[2]float64{12, -68}, [2]float64{60, -25},
		[2]float64{10, -39}, // a fade 14 dB below the music, far above the hiss
		[2]float64{60, -25}, [2]float64{5, -68}, [2]float64{60, -25}, [2]float64{10, -68},
	)
	got := Detect(env)
	if len(got) != 6 {
		t.Fatalf("got %v, want 3 tracks", got)
	}
	if end, start := got[1].PositionMs, got[2].PositionMs; end < 72000 || end > 82000 || start-end > 100 || start <= end {
		t.Fatalf("fade cut: end %d, start %d", end, start)
	}
	for i, want := range map[int]int64{0: 11750, 3: 142250, 4: 146750, 5: 207250} {
		if got[i].PositionMs != want {
			t.Fatalf("hiss gap marker %d: got %d want %d", i, got[i].PositionMs, want)
		}
	}
}

func TestDetectSharpDropIntoQuieterSong(t *testing.T) {
	env := build(
		[2]float64{12, -68}, [2]float64{60, -22},
		[2]float64{4, -40}, // the song stops dead, the next one starts quieter
		[2]float64{60, -32}, [2]float64{10, -68},
	)
	if got := tracks(t, env); got != 2 {
		t.Fatalf("got %d tracks, want 2", got)
	}
}

func TestDetectShortQuietBarIsNotACut(t *testing.T) {
	env := build(
		[2]float64{12, -68}, [2]float64{60, -25},
		[2]float64{5.5, -37}, // 12 dB below the music on both sides, too short for a fade
		[2]float64{60, -25}, [2]float64{10, -68},
	)
	if got := tracks(t, env); got != 0 {
		t.Fatalf("got %d tracks, want no split", got)
	}
}

// TestDetectRealRecording runs against a real side when T2D_SPLIT_SAMPLE names a WAV file.
func TestDetectRealRecording(t *testing.T) {
	path := os.Getenv("T2D_SPLIT_SAMPLE")
	if path == "" {
		t.Skip("T2D_SPLIT_SAMPLE is not set")
	}
	env, err := wav.Envelope(context.Background(), path, WindowMs)
	if err != nil {
		t.Fatal(err)
	}
	got := Detect(env)
	for i := 0; i+1 < len(got); i += 2 {
		t.Logf("track %d: %.1f s - %.1f s", i/2+1, float64(got[i].PositionMs)/1000, float64(got[i+1].PositionMs)/1000)
	}
	if want := os.Getenv("T2D_SPLIT_TRACKS"); want != "" && want != strconv.Itoa(len(got)/2) {
		t.Fatalf("found %d tracks, want %s", len(got)/2, want)
	}
}
