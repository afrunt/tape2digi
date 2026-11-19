package lookup

import (
	"strings"
	"unicode"

	"github.com/afrunt/tape2digi/internal/project"
)

// Mapping is a release's tracks placed onto tape sides A and B.
type Mapping struct {
	A []Track `json:"a"`
	B []Track `json:"b"`
}

// DefaultMedia picks the media to map: the first two, or the only one.
func DefaultMedia(d Detail) []int {
	switch len(d.Media) {
	case 0:
		return nil
	case 1:
		return []int{0}
	}
	return []int{0, 1}
}

// Map places the chosen media of a release onto the sides. Two media go to A
// and B in order. One medium is split by an A/B prefix in the track numbers
// when present, otherwise at the point whose part lengths best match the
// recorded side lengths (durA, durB in ms; durB 0 puts everything on A).
func Map(d Detail, media []int, durA, durB int64) Mapping {
	var picked []Medium
	for _, i := range media {
		if i >= 0 && i < len(d.Media) {
			picked = append(picked, d.Media[i])
		}
	}
	switch len(picked) {
	case 0:
		return Mapping{}
	case 1:
	default:
		return Mapping{A: picked[0].Tracks, B: picked[1].Tracks}
	}
	tracks := picked[0].Tracks
	if m, ok := byPrefix(tracks); ok {
		return m
	}
	if durB <= 0 || len(tracks) < 2 {
		return Mapping{A: tracks}
	}
	var total int64
	for _, t := range tracks {
		total += t.LengthMs
	}
	best, bestErr := len(tracks), int64(-1)
	var sum int64
	for k := 0; k <= len(tracks); k++ {
		if k > 0 {
			sum += tracks[k-1].LengthMs
		}
		e := abs(sum-durA) + abs(total-sum-durB)
		if bestErr < 0 || e < bestErr {
			best, bestErr = k, e
		}
	}
	return Mapping{A: tracks[:best], B: tracks[best:]}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// byPrefix splits tracks numbered like A1..A5, B1..B4.
func byPrefix(tracks []Track) (Mapping, bool) {
	var m Mapping
	for _, t := range tracks {
		n := strings.ToUpper(strings.TrimSpace(t.Number))
		if len(n) < 2 || !unicode.IsDigit(rune(n[1])) {
			return Mapping{}, false
		}
		switch n[0] {
		case 'A':
			m.A = append(m.A, t)
		case 'B':
			m.B = append(m.B, t)
		default:
			return Mapping{}, false
		}
	}
	return m, len(m.A) > 0 && len(m.B) > 0
}

// Listing converts mapped tracks into the listing stored with a side. A track
// artist equal to the release artist is left empty so it follows the project.
func Listing(tracks []Track, releaseArtist string) []project.ListingTrack {
	out := make([]project.ListingTrack, 0, len(tracks))
	for _, t := range tracks {
		a := t.Artist
		if strings.EqualFold(a, releaseArtist) {
			a = ""
		}
		out = append(out, project.ListingTrack{Title: t.Title, Artist: a, LengthMs: t.LengthMs})
	}
	return out
}

// TitleMarkers fills the titles (and differing artists) of the tracks opened
// by the start markers, in order. Recorded tracks beyond the listing keep
// their details. It returns the new list and how many listing entries had no
// recorded track.
func TitleMarkers(list []project.Marker, listing []project.ListingTrack) ([]project.Marker, int) {
	out := append([]project.Marker(nil), list...)
	n := 0
	for i := range out {
		if out[i].Kind != project.MarkerStart {
			continue
		}
		if n >= len(listing) {
			break
		}
		out[i].Title = listing[n].Title
		if listing[n].Artist != "" {
			out[i].Artist = listing[n].Artist
		}
		n++
	}
	return out, len(listing) - n
}

// VariousArtists is the MusicBrainz artist of compilations.
const VariousArtists = "Various Artists"

// FillDetails returns the project details with empty fields taken from the
// release; filled fields are never changed.
func FillDetails(cur project.Details, r Release) project.Details {
	if cur.Artist == "" && !cur.Compilation {
		if strings.EqualFold(r.Artist, VariousArtists) {
			cur.Compilation = true
		} else {
			cur.Artist = r.Artist
		}
	}
	if cur.Album == "" {
		cur.Album = r.Title
	}
	if cur.Year == "" && len(r.Date) >= 4 {
		cur.Year = r.Date[:4]
	}
	return cur
}
