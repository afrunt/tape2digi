package i18n

import (
	"errors"
	"regexp"
	"testing"
)

var verb = regexp.MustCompile(`%[a-z]`)

func TestCatalogsMatch(t *testing.T) {
	if len(en) != len(uk) {
		t.Fatalf("en has %d keys, uk has %d", len(en), len(uk))
	}
	for k, e := range en {
		u, ok := uk[k]
		if !ok {
			t.Fatalf("uk misses %q", k)
		}
		a, b := verb.FindAllString(e, -1), verb.FindAllString(u, -1)
		if len(a) != len(b) {
			t.Fatalf("%q: verbs differ: %v vs %v", k, a, b)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%q: verbs differ: %v vs %v", k, a, b)
			}
		}
	}
}

func TestLocaleFallbackAndLazyErrors(t *testing.T) {
	t.Cleanup(func() { SetLocale(English) })
	SetLocale("fr")
	if Locale() != English {
		t.Fatal("unknown locale must select English")
	}
	if T("no.such.key") != "no.such.key" {
		t.Fatal("missing key must fall back to the key")
	}
	inner := errors.New("boom")
	sentinel := New("recorder.writeFailed", inner)
	if !errors.Is(sentinel, inner) {
		t.Fatal("error must unwrap its error argument")
	}
	en1 := sentinel.Error()
	SetLocale(Ukrainian)
	if sentinel.Error() == en1 {
		t.Fatal("an existing error must follow a later language switch")
	}
	if T("project.label.artist") != "ім'я виконавця" {
		t.Fatal("uk lookup failed")
	}
}
