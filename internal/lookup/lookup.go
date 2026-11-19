// Package lookup finds cassette track listings on MusicBrainz and front covers
// on the Cover Art Archive. It sends only the search text the user typed.
package lookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afrunt/tape2digi/internal/i18n"
)

// Default service addresses.
const (
	MusicBrainzURL = "https://musicbrainz.org/ws/2"
	CoverArtURL    = "https://coverartarchive.org"
	// MaxCoverBytes is the largest cover image accepted.
	MaxCoverBytes = 20 << 20
	searchLimit   = 25
)

// Release is one search result.
type Release struct {
	MBID       string   `json:"mbid"`
	Title      string   `json:"title"`
	Artist     string   `json:"artist"`
	Date       string   `json:"date"`
	Country    string   `json:"country"`
	Formats    []string `json:"formats"`
	TrackCount int      `json:"trackCount"`
}

// Track is one track of a medium.
type Track struct {
	Position int    `json:"position"`
	Number   string `json:"number"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	LengthMs int64  `json:"lengthMs"`
}

// Medium is one disc, tape or side of a release.
type Medium struct {
	Position int     `json:"position"`
	Format   string  `json:"format"`
	Title    string  `json:"title"`
	Tracks   []Track `json:"tracks"`
}

// Detail is a release with its tracks.
type Detail struct {
	Release
	Media []Medium `json:"media"`
}

// Client talks to MusicBrainz at most once per second.
type Client struct {
	BaseURL   string
	CoverURL  string
	UserAgent string
	HTTP      *http.Client

	// now and sleep are replaced in tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu   sync.Mutex
	last time.Time
}

// New returns a client that identifies itself with the app version.
func New(version string) *Client {
	return &Client{
		BaseURL:   MusicBrainzURL,
		CoverURL:  CoverArtURL,
		UserAgent: fmt.Sprintf("tape2digi/%s ( https://github.com/afrunt/tape2digi )", version),
		HTTP:      &http.Client{Timeout: 15 * time.Second},
		now:       time.Now,
		sleep:     sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// wait keeps requests at least one second apart.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.last.IsZero() {
		if d := time.Second - c.now().Sub(c.last); d > 0 {
			if err := c.sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	c.last = c.now()
	return nil
}

// ErrNotFound is returned when nothing matches.
var ErrNotFound = i18n.New("lookup.nothingFound")

var errTooLarge = errors.New("too large")

// get fetches u, retrying once when the service says it is busy.
func (c *Client) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	b, err := c.getOnce(ctx, u, limit)
	if errors.Is(err, errBusy) {
		if err := c.sleep(ctx, time.Second); err != nil {
			return nil, err
		}
		b, err = c.getOnce(ctx, u, limit)
	}
	return b, err
}

var errBusy = i18n.New("lookup.busy")

func (c *Client) getOnce(ctx context.Context, u string, limit int64) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, i18n.New("lookup.network", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errBusy
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, i18n.New("lookup.status", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, i18n.New("lookup.network", err)
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

func quote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.TrimSpace(s))
	return `"` + s + `"`
}

// Search finds releases by artist and album; cassette releases come first.
func (c *Client) Search(ctx context.Context, artist, album string) ([]Release, error) {
	var parts []string
	if strings.TrimSpace(artist) != "" {
		parts = append(parts, "artist:"+quote(artist))
	}
	if strings.TrimSpace(album) != "" {
		parts = append(parts, "release:"+quote(album))
	}
	if len(parts) == 0 {
		return nil, i18n.New("lookup.enterArtistOrAlbum")
	}
	q := url.Values{"query": {strings.Join(parts, " AND ")}, "fmt": {"json"}, "limit": {fmt.Sprint(searchLimit)}}
	b, err := c.get(ctx, c.BaseURL+"/release/?"+q.Encode(), 4<<20)
	if err != nil {
		return nil, err
	}
	var r struct {
		Releases []mbRelease `json:"releases"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, i18n.New("lookup.badAnswer")
	}
	out := make([]Release, 0, len(r.Releases))
	for _, x := range r.Releases {
		out = append(out, x.release())
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	sort.SliceStable(out, func(i, j int) bool { return isCassette(out[i]) && !isCassette(out[j]) })
	return out, nil
}

func isCassette(r Release) bool {
	for _, f := range r.Formats {
		if strings.EqualFold(f, "Cassette") {
			return true
		}
	}
	return false
}

// Release fetches a release with its media and tracks.
func (c *Client) Release(ctx context.Context, mbid string) (Detail, error) {
	if !validMBID(mbid) {
		return Detail{}, ErrNotFound
	}
	q := url.Values{"inc": {"recordings artist-credits media"}, "fmt": {"json"}}
	b, err := c.get(ctx, c.BaseURL+"/release/"+mbid+"?"+q.Encode(), 4<<20)
	if err != nil {
		return Detail{}, err
	}
	var r mbRelease
	if err := json.Unmarshal(b, &r); err != nil {
		return Detail{}, i18n.New("lookup.badAnswer")
	}
	d := Detail{Release: r.release()}
	for i, m := range r.Media {
		md := Medium{Position: m.Position, Format: m.Format, Title: m.Title}
		if md.Position == 0 {
			md.Position = i + 1
		}
		for j, t := range m.Tracks {
			tr := Track{Position: t.Position, Number: t.Number, Title: t.Title, Artist: credit(t.ArtistCredit)}
			if tr.Position == 0 {
				tr.Position = j + 1
			}
			if tr.Title == "" && t.Recording != nil {
				tr.Title = t.Recording.Title
			}
			if t.Length != nil {
				tr.LengthMs = *t.Length
			} else if t.Recording != nil && t.Recording.Length != nil {
				tr.LengthMs = *t.Recording.Length
			}
			md.Tracks = append(md.Tracks, tr)
		}
		d.Media = append(d.Media, md)
	}
	return d, nil
}

// Cover downloads the front cover of a release (PNG or JPEG, at most 20 MB)
// and returns the image and its file extension.
func (c *Client) Cover(ctx context.Context, mbid string) ([]byte, string, error) {
	if !validMBID(mbid) {
		return nil, "", ErrNotFound
	}
	b, err := c.get(ctx, c.CoverURL+"/release/"+mbid+"/front-500", MaxCoverBytes)
	switch {
	case errors.Is(err, errTooLarge):
		return nil, "", i18n.New("lookup.coverTooLarge")
	case errors.Is(err, ErrNotFound):
		return nil, "", i18n.New("lookup.noCover")
	case err != nil:
		return nil, "", err
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return b, ".png", nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return b, ".jpg", nil
	}
	return nil, "", i18n.New("lookup.badCover")
}

func validMBID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

type mbCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
}

func credit(cs []mbCredit) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return strings.TrimSpace(b.String())
}

type mbTrack struct {
	Position     int        `json:"position"`
	Number       string     `json:"number"`
	Title        string     `json:"title"`
	Length       *int64     `json:"length"`
	ArtistCredit []mbCredit `json:"artist-credit"`
	Recording    *struct {
		Title  string `json:"title"`
		Length *int64 `json:"length"`
	} `json:"recording"`
}

type mbMedium struct {
	Position   int       `json:"position"`
	Format     string    `json:"format"`
	Title      string    `json:"title"`
	TrackCount int       `json:"track-count"`
	Tracks     []mbTrack `json:"tracks"`
}

type mbRelease struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Date         string     `json:"date"`
	Country      string     `json:"country"`
	ArtistCredit []mbCredit `json:"artist-credit"`
	TrackCount   int        `json:"track-count"`
	Media        []mbMedium `json:"media"`
}

func (r mbRelease) release() Release {
	out := Release{MBID: r.ID, Title: r.Title, Artist: credit(r.ArtistCredit), Date: r.Date, Country: r.Country, TrackCount: r.TrackCount}
	n := 0
	for _, m := range r.Media {
		out.Formats = append(out.Formats, m.Format)
		n += max(m.TrackCount, len(m.Tracks))
	}
	if out.TrackCount == 0 {
		out.TrackCount = n
	}
	return out
}
