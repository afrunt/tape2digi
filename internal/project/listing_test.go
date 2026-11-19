package project

import "testing"

func TestListingIsStagedSavedAndDiscarded(t *testing.T) {
	p := newSavedSide(t)
	if err := p.SetListing("B", []ListingTrack{{Title: "x"}}); err == nil {
		t.Fatal("listing for a side without audio accepted")
	}
	list := []ListingTrack{{Title: " One\n", LengthMs: 1000}, {Title: "Two", Artist: "Guest", LengthMs: -5}}
	if err := p.SetListing("A", list); err != nil {
		t.Fatal(err)
	}
	info := p.Info()
	got := info.Sides["A"].Listing
	if !info.Dirty || len(got) != 2 || got[0].Title != "One" || got[1].LengthMs != 0 {
		t.Fatalf("staged %+v dirty=%v", got, info.Dirty)
	}
	p.Discard()
	if len(p.Listing("A")) != 0 {
		t.Fatal("discard kept the listing")
	}
	if err := p.SetListing("A", list); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := Open(p.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if l := q.Listing("A"); len(l) != 2 || l[1].Artist != "Guest" || q.Info().Dirty {
		t.Fatalf("reopened %+v", l)
	}
}
