package lookup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/project"
)

const mbid = "22222222-2222-2222-2222-222222222222"

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *fakeClock) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("1.2.3")
	c.BaseURL, c.CoverURL = srv.URL+"/ws/2", srv.URL
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c.now = func() time.Time { return clk.t }
	c.sleep = func(_ context.Context, d time.Duration) error {
		clk.slept = append(clk.slept, d)
		clk.t = clk.t.Add(d)
		return nil
	}
	return c, clk
}

func fixture(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("testdata/" + name)
		_, _ = w.Write(b)
	}
}

func TestSearchParsesAndPutsCassettesFirst(t *testing.T) {
	var ua, query string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		ua, query = r.UserAgent(), r.URL.Query().Get("query")
		fixture("search.json")(w, r)
	})
	got, err := c.Search(context.Background(), `Art"ist`, "Album")
	if err != nil {
		t.Fatal(err)
	}
	if ua != "tape2digi/1.2.3 ( https://github.com/afrunt/tape2digi )" {
		t.Fatalf("user agent %q", ua)
	}
	if query != `artist:"Art\"ist" AND release:"Album"` {
		t.Fatalf("query %q", query)
	}
	if len(got) != 2 || got[0].MBID != mbid || got[0].Artist != "Artist & Friend" || got[0].Formats[0] != "Cassette" || got[0].TrackCount != 9 {
		t.Fatalf("%+v", got)
	}
}

func TestReleaseParsesTracks(t *testing.T) {
	c, _ := testClient(t, fixture("release.json"))
	d, err := c.Release(context.Background(), mbid)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Media) != 1 || len(d.Media[0].Tracks) != 4 {
		t.Fatalf("%+v", d)
	}
	tr := d.Media[0].Tracks[1]
	if tr.Number != "A2" || tr.Title != "Two" || tr.Artist != "Guest" || tr.LengthMs != 200000 {
		t.Fatalf("track %+v (length falls back to the recording)", tr)
	}
}

func TestRequestsAreOneSecondApart(t *testing.T) {
	c, clk := testClient(t, fixture("search.json"))
	for i := 0; i < 2; i++ {
		if _, err := c.Search(context.Background(), "a", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(clk.slept) != 1 || clk.slept[0] != time.Second {
		t.Fatalf("slept %v", clk.slept)
	}
}

func TestBusyIsRetriedOnce(t *testing.T) {
	calls := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture("search.json")(w, r)
	})
	if _, err := c.Search(context.Background(), "a", ""); err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestErrorMapping(t *testing.T) {
	status := http.StatusServiceUnavailable
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
	if _, err := c.Search(context.Background(), "a", "b"); err == nil || err.Error() != i18n.T("lookup.busy") {
		t.Fatalf("503: %v", err)
	}
	status = http.StatusNotFound
	if _, err := c.Release(context.Background(), mbid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.Search(context.Background(), " ", ""); err == nil {
		t.Fatal("empty search accepted")
	}
	c2, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"releases":[]}`)) })
	if _, err := c2.Search(context.Background(), "a", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty result: %v", err)
	}
	c3 := New("x")
	c3.BaseURL = "http://127.0.0.1:1"
	if _, err := c3.Search(context.Background(), "a", ""); err == nil || !strings.HasPrefix(err.Error(), strings.TrimSuffix(i18n.T("lookup.network", ""), " ")) {
		t.Fatalf("network: %v", err)
	}
}

func TestCoverChecksTypeAndSize(t *testing.T) {
	body := []byte("\x89PNG\r\n\x1a\nrest")
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release/"+mbid+"/front-500" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(body)
	})
	if b, ext, err := c.Cover(context.Background(), mbid); err != nil || ext != ".png" || len(b) != len(body) {
		t.Fatalf("png: %v %q", err, ext)
	}
	body = []byte("<html>")
	if _, _, err := c.Cover(context.Background(), mbid); err == nil {
		t.Fatal("html accepted as cover")
	}
	body = make([]byte, MaxCoverBytes+10)
	copy(body, []byte{0xFF, 0xD8, 0xFF})
	if _, _, err := c.Cover(context.Background(), mbid); err == nil {
		t.Fatal("oversized cover accepted")
	}
}

func tracks(lengths ...int64) []Track {
	var out []Track
	for i, l := range lengths {
		out = append(out, Track{Position: i + 1, Title: string(rune('a' + i)), LengthMs: l})
	}
	return out
}

func TestMapRules(t *testing.T) {
	two := Detail{Media: []Medium{{Tracks: tracks(1, 2)}, {Tracks: tracks(3)}}}
	if m := Map(two, DefaultMedia(two), 0, 0); len(m.A) != 2 || len(m.B) != 1 {
		t.Fatalf("two media %+v", m)
	}
	numbered := Detail{Media: []Medium{{Tracks: []Track{{Number: "A1"}, {Number: "A2"}, {Number: "B1"}}}}}
	if m := Map(numbered, []int{0}, 0, 0); len(m.A) != 2 || len(m.B) != 1 {
		t.Fatalf("A/B numbers %+v", m)
	}
	one := Detail{Media: []Medium{{Tracks: tracks(100, 100, 100, 300)}}}
	if m := Map(one, []int{0}, 300, 300); len(m.A) != 3 || len(m.B) != 1 {
		t.Fatalf("duration split %+v", m)
	}
	if m := Map(one, []int{0}, 600, 0); len(m.A) != 4 || len(m.B) != 0 {
		t.Fatalf("one side %+v", m)
	}
}

func TestTitleMarkersAndFillDetails(t *testing.T) {
	list := []project.Marker{{Kind: "start", PositionMs: 0, Title: "old"}, {Kind: "end", PositionMs: 10}, {Kind: "start", PositionMs: 20}, {Kind: "end", PositionMs: 30}, {Kind: "start", PositionMs: 40, Title: "keep"}}
	out, unmatched := TitleMarkers(list, []project.ListingTrack{{Title: "One"}, {Title: "Two", Artist: "Guest"}})
	if out[0].Title != "One" || out[2].Title != "Two" || out[2].Artist != "Guest" || out[4].Title != "keep" || unmatched != 0 || list[0].Title != "old" {
		t.Fatalf("%+v %d", out, unmatched)
	}
	if _, unmatched := TitleMarkers(list[:2], []project.ListingTrack{{Title: "1"}, {Title: "2"}}); unmatched != 1 {
		t.Fatalf("unmatched %d", unmatched)
	}
	r := Release{Artist: "Artist", Title: "Album", Date: "1994-05-01"}
	if d := FillDetails(project.Details{Album: "Mine"}, r); d.Artist != "Artist" || d.Album != "Mine" || d.Year != "1994" {
		t.Fatalf("%+v", d)
	}
	if d := FillDetails(project.Details{}, Release{Artist: VariousArtists}); !d.Compilation || d.Artist != "" {
		t.Fatalf("compilation %+v", d)
	}
	if l := Listing([]Track{{Title: "x", Artist: "artist"}, {Title: "y", Artist: "Guest"}}, "Artist"); l[0].Artist != "" || l[1].Artist != "Guest" {
		t.Fatalf("listing %+v", l)
	}
}
