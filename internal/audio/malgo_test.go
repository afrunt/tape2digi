package audio

import "testing"

func TestMalgoSourceListsDevices(t *testing.T) {
	src, err := NewMalgoSource()
	if err != nil {
		t.Skipf("no audio backend: %v", err)
	}
	defer src.Close()
	devs, err := src.CaptureDevices()
	if err != nil {
		t.Skipf("cannot enumerate: %v", err)
	}
	t.Logf("%d capture devices", len(devs))
	for _, d := range devs {
		if d.Name == "" || d.ID == "" {
			t.Fatalf("incomplete device %+v", d)
		}
	}
}
