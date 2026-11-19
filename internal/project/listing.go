package project

import "github.com/afrunt/tape2digi/internal/i18n"

// maxListing is the longest track listing kept for a side.
const maxListing = 99

// ListingTrack is one entry of a side's looked-up track listing.
type ListingTrack struct {
	Title    string `json:"title"`
	Artist   string `json:"artist,omitempty"`
	LengthMs int64  `json:"lengthMs"`
}

func (p *Project) effectiveListingLocked(key string) []ListingTrack {
	if d, ok := p.draftListing[key]; ok {
		return d
	}
	if s, ok := p.staged[key]; ok {
		return s.Listing
	}
	return p.sides[key].Listing
}

// withDraftsLocked returns s with the side's staged markers and listing.
func (p *Project) withDraftsLocked(key string, s Side) Side {
	if d, ok := p.draftMarkers[key]; ok {
		s.Markers = d
	}
	if d, ok := p.draftListing[key]; ok {
		s.Listing = d
	}
	return s
}

// Listing returns the side's track listing; nil when there is none.
func (p *Project) Listing(key string) []ListingTrack {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ListingTrack(nil), p.effectiveListingLocked(key)...)
}

// SetListing stages the side's track listing (written by Save); an empty list
// removes it.
func (p *Project) SetListing(key string, list []ListingTrack) error {
	if !ValidSide(key) {
		return i18n.New("project.unknownSide", key)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sideLocked(key); !ok {
		return i18n.New("project.sideHasNoAudio", key)
	}
	if len(list) > maxListing {
		list = list[:maxListing]
	}
	clean := make([]ListingTrack, 0, len(list))
	for _, t := range list {
		t.Title, t.Artist = repairText(t.Title), repairText(t.Artist)
		t.LengthMs = max(t.LengthMs, 0)
		clean = append(clean, t)
	}
	if len(clean) == 0 {
		clean = nil
	}
	if p.draftListing == nil {
		p.draftListing = map[string][]ListingTrack{}
	}
	p.draftListing[key] = clean
	return nil
}
