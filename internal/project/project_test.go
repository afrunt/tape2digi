package project

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/afrunt/tape2digi/internal/i18n"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afrunt/tape2digi/internal/appdata"
)

func writeImage(t *testing.T, path string, jpg bool) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.White)
	var buf bytes.Buffer
	if jpg {
		_ = jpeg.Encode(&buf, img, nil)
	} else {
		_ = png.Encode(&buf, img)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOpenRoundTrip(t *testing.T) {
	parent := t.TempDir()
	p, err := Create(parent, "Касета: 1/2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir() != parent {
		t.Fatalf("dir %s", p.Dir())
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), "recordings")); err == nil {
		t.Fatal("v2 project must not have a recordings folder")
	}
	side := Side{Origin: OriginRecorded, RecordedAt: time.Now().UTC(), DurationMs: 1000, SampleRate: 44100, Channels: 2, BitDepth: 16, Device: "USB"}
	if err := p.SetSide("A", side); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSide("C", side); err == nil {
		t.Fatal("unknown side accepted")
	}
	q, err := Open(p.Dir())
	if err != nil {
		t.Fatal(err)
	}
	info := q.Info()
	if info.Name != "Касета: 1/2" || len(info.Sides) != 1 || !info.Sides["A"].Missing || info.Sides["A"].File != "side-A.wav" {
		t.Fatalf("%+v", info)
	}
	if !q.SideExists("A") || q.SideExists("B") {
		t.Fatal("SideExists")
	}
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-B.wav"), []byte("x"), 0o644)
	if !q.SideExists("B") {
		t.Fatal("unregistered file on disk must count as existing")
	}
}

func TestOpenV1UpgradesOnSave(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(`{"schemaVersion":1,"name":"old","recordings":[{"file":"recordings/a.wav"}]}`), 0o644)
	p, err := Open(dir)
	if err != nil || len(p.Info().Sides) != 0 {
		t.Fatalf("%v %+v", err, p.Info())
	}
	if err := p.SetSide("A", Side{Origin: OriginImported}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, DescriptorName))
	if !strings.Contains(string(b), `"schemaVersion": 2`) || !strings.Contains(string(b), "recordings/a.wav") {
		t.Fatalf("%s", b)
	}
}

func TestCreateRejectsNonEmptyTarget(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "X")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	if _, err := Create(dir, "X"); err == nil {
		t.Fatal("expected error for non-empty dir")
	}
	empty := filepath.Join(parent, "Y")
	_ = os.MkdirAll(empty, 0o755)
	if _, err := Create(empty, "Y"); err != nil {
		t.Fatalf("empty dir should be accepted: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Fatal("missing descriptor")
	}
	write := func(s string) { _ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(s), 0o644) }
	write("{oops")
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt")
	}
	write(`{"schemaVersion": 99, "name": "x"}`)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("newer version: %v", err)
	}
	write(`{"schemaVersion": 1}`)
	p, err := Open(dir)
	if err != nil || p.Name() != filepath.Base(dir) {
		t.Fatalf("default name: %v %v", err, p)
	}
}

func TestUnknownFieldsPreserved(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(`{"schemaVersion":1,"name":"a","future":{"k":1}}`), 0o644)
	p, _ := Open(dir)
	if err := p.Rename("b"); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, DescriptorName))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if string(m["future"]) != `{"k":1}` && !strings.Contains(string(m["future"]), `"k": 1`) && !strings.Contains(string(m["future"]), `"k":1`) {
		t.Fatalf("future lost: %s", b)
	}
}

func TestRenameValidation(t *testing.T) {
	p, _ := Create(t.TempDir(), "A")
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("x", 201)} {
		if err := p.Rename(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := p.Rename("  Нова  "); err != nil || p.Name() != "Нова" {
		t.Fatalf("rename: %v %q", err, p.Name())
	}
}

func TestStagedEditsSaveAndDiscard(t *testing.T) {
	p, _ := Create(t.TempDir(), "A")
	src := t.TempDir()
	png1, jpg1 := filepath.Join(src, "a.png"), filepath.Join(src, "b.jpg")
	writeImage(t, png1, false)
	writeImage(t, jpg1, true)

	_ = p.Rename("Нова")
	if err := p.SetCover(png1); err != nil {
		t.Fatal(err)
	}
	info := p.Info()
	if !info.Dirty || info.Name != "Нова" || !info.CoverDraft {
		t.Fatalf("%+v", info)
	}
	if data, ct := p.CoverData(); len(data) == 0 || ct != "image/png" {
		t.Fatal("draft cover not served")
	}
	if q, _ := Open(p.Dir()); q.Name() != "A" {
		t.Fatal("staged name leaked to disk")
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), "cover.png")); err == nil {
		t.Fatal("staged cover written before Save")
	}
	p.Discard()
	if p.Dirty() || p.Name() != "A" {
		t.Fatal("discard")
	}

	_ = p.Rename("Нова")
	_ = p.SetCover(png1)
	if err := p.Save(); err != nil || p.Dirty() {
		t.Fatalf("save: %v", err)
	}
	q, _ := Open(p.Dir())
	if q.Name() != "Нова" || q.Info().Cover != "cover.png" {
		t.Fatalf("%+v", q.Info())
	}
	_ = p.SetCover(jpg1)
	_ = p.Save()
	if _, err := os.Stat(filepath.Join(p.Dir(), "cover.png")); err == nil {
		t.Fatal("old cover not removed")
	}
	if _, err := os.Stat(png1); err != nil {
		t.Fatal("original must stay")
	}
	_ = p.ClearCover()
	if data, _ := p.CoverData(); data != nil {
		t.Fatal("cleared cover still served")
	}
	_ = p.Save()
	if _, err := os.Stat(filepath.Join(p.Dir(), "cover.jpg")); err == nil || p.Info().Cover != "" {
		t.Fatal("cover not removed")
	}

	txt := filepath.Join(src, "x.png")
	_ = os.WriteFile(txt, []byte("not an image"), 0o644)
	if err := p.SetCover(txt); err == nil {
		t.Fatal("non-image accepted")
	}
	big := filepath.Join(src, "big.png")
	_ = os.WriteFile(big, make([]byte, MaxCoverBytes+1), 0o644)
	if err := p.SetCover(big); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestMissingCoverIsNotAnOpenFailure(t *testing.T) {
	p, _ := Create(t.TempDir(), "A")
	src := filepath.Join(t.TempDir(), "c.png")
	writeImage(t, src, false)
	_ = p.SetCover(src)
	_ = p.Save()
	_ = os.Remove(filepath.Join(p.Dir(), "cover.png"))
	q, err := Open(p.Dir())
	if data, _ := q.CoverData(); err != nil || !q.Info().CoverMissing || data != nil {
		t.Fatalf("%v %+v", err, q.Info())
	}
}

func TestMovedProjectStillOpens(t *testing.T) {
	p, _ := Create(t.TempDir(), "A")
	_ = p.SetSide("A", Side{Origin: OriginRecorded})
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-A.wav"), []byte("x"), 0o644)
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(p.Dir(), moved); err != nil {
		t.Fatal(err)
	}
	q, err := Open(moved)
	if err != nil || q.Info().Sides["A"].Missing {
		t.Fatalf("%v %+v", err, q.Info())
	}
}

func TestSaveAs(t *testing.T) {
	p, _ := Create(t.TempDir(), "Orig")
	src := filepath.Join(t.TempDir(), "c.png")
	writeImage(t, src, false)
	_ = p.SetCover(src)
	_ = p.Save()
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-A.wav"), []byte("audio"), 0o644)
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-B.wav.part"), []byte("partial"), 0o644)
	_ = os.WriteFile(filepath.Join(p.Dir(), "notes.txt"), []byte("n"), 0o644)
	_ = p.SetSide("A", Side{Origin: OriginRecorded})
	_ = p.Rename("Копія")

	target := t.TempDir()
	var last int64
	q, err := p.SaveAs(context.Background(), target, "Копія", func(d, total int64) { last = d })
	if err != nil {
		t.Fatal(err)
	}
	if last == 0 || q.Name() != "Копія" || q.Dir() != target {
		t.Fatalf("%v %s", last, q.Dir())
	}
	for _, f := range []string{"side-A.wav", "cover.png", DescriptorName} {
		if _, err := os.Stat(filepath.Join(q.Dir(), f)); err != nil {
			t.Fatalf("missing %s", f)
		}
	}
	for _, f := range []string{"side-B.wav.part", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(q.Dir(), f)); err == nil {
			t.Fatalf("%s must not be copied", f)
		}
	}
	if o, _ := Open(p.Dir()); o.Name() != "Orig" {
		t.Fatal("original changed")
	}
	if !p.Dirty() {
		t.Fatal("original must keep its staged edits")
	}
	if _, err := p.SaveAs(context.Background(), target, "Копія", nil); err == nil {
		t.Fatal("non-empty target accepted")
	}
}

func TestSaveAsFailureAndCancelCleanUp(t *testing.T) {
	p, _ := Create(t.TempDir(), "Orig")
	_ = os.WriteFile(filepath.Join(p.Dir(), "side-A.wav"), []byte("audio"), 0o644)
	_ = p.SetSide("A", Side{Origin: OriginRecorded})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := t.TempDir()
	if _, err := p.SaveAs(ctx, target, "X", nil); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := os.Stat(filepath.Join(target, "X")); err == nil {
		t.Fatal("partial copy left behind")
	}
}

func TestLeftovers(t *testing.T) {
	p, _ := Create(t.TempDir(), "A")
	if len(p.Leftovers()) != 0 {
		t.Fatal("unexpected leftover")
	}
	path, _ := p.PartPath("B")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("junk"), 0o644)
	l := p.Leftovers()
	if len(l) != 1 || l[0].Side != "B" || !p.HasPart("B") {
		t.Fatalf("%+v", l)
	}
	_ = p.RemovePart("B")
	if p.HasPart("B") {
		t.Fatal("part not removed")
	}
}

func TestRecents(t *testing.T) {
	dir := appdata.Dir{Path: filepath.Join(t.TempDir(), ".tape2digi")}
	r := NewRecents(dir)
	a, _ := Create(t.TempDir(), "A")
	b, _ := Create(t.TempDir(), "B")
	_ = r.Touch(a.Dir(), "A")
	time.Sleep(5 * time.Millisecond)
	_ = r.Touch(b.Dir(), "B")
	l := r.List()
	if len(l) != 2 || l[0].Name != "B" || !l[0].Available {
		t.Fatalf("%+v", l)
	}
	_ = os.RemoveAll(a.Dir())
	l = r.List()
	if len(l) != 2 || l[1].Available {
		t.Fatalf("stale entry must stay, unavailable: %+v", l)
	}
	_ = r.SyncName(b.Dir(), "B2")
	if r.List()[0].Name != "B2" {
		t.Fatal("name not synced")
	}
	_ = r.Remove(a.Dir())
	if len(r.List()) != 1 {
		t.Fatal("remove failed")
	}
	for i := 0; i < 15; i++ {
		_ = r.Touch(filepath.Join("/nope", string(rune('a'+i))), "x")
	}
	if len(r.List()) != maxRecent {
		t.Fatalf("limit: %d", len(r.List()))
	}
}

func TestRenameBackToSavedNameClearsDirty(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(`{"schemaVersion":1,"name":"a"}`), 0o644)
	p, _ := Open(dir)
	_ = p.Rename("ab")
	if !p.Info().Dirty {
		t.Fatal("expected dirty after rename")
	}
	if err := p.Rename("  a "); err != nil || p.Info().Dirty {
		t.Fatalf("reverting must clear dirty: %v", err)
	}
}

func stageTake(t *testing.T, p *Project, key, content string) {
	t.Helper()
	part, _ := p.PartPath(key)
	_ = os.MkdirAll(filepath.Dir(part), 0o755)
	if err := os.WriteFile(part, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.StageSide(key, part, Side{Origin: OriginRecorded, DurationMs: 10000}); err != nil {
		t.Fatal(err)
	}
}

func TestStagedSideAppliesOnlyOnSave(t *testing.T) {
	p, err := Create(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(p.Dir(), "side-A.wav")
	_ = os.WriteFile(final, []byte("old"), 0o644)
	_ = p.SetSide("A", Side{Origin: OriginRecorded})
	stageTake(t, p, "A", "new")
	if !p.Info().Dirty || !p.Info().Sides["A"].Staged {
		t.Fatal("staged side must mark dirty")
	}
	if b, _ := os.ReadFile(final); string(b) != "old" {
		t.Fatalf("project file changed before save: %s", b)
	}
	if err := p.Save(); err != nil || p.Info().Dirty {
		t.Fatalf("save: %v", err)
	}
	if b, _ := os.ReadFile(final); string(b) != "new" {
		t.Fatalf("save did not apply: %s", b)
	}
	if p.Info().Sides["A"].Staged {
		t.Fatal("still staged after save")
	}
}

func TestDiscardRemovesStagedSide(t *testing.T) {
	p, _ := Create(t.TempDir(), "p")
	stageTake(t, p, "B", "x")
	p.Discard()
	if p.Info().Dirty || p.SideExists("B") {
		t.Fatal("discard must drop staged side")
	}
	if w, _ := p.AudioPath("B"); w != filepath.Join(p.Dir(), "side-B.wav") {
		t.Fatal(w)
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), StagingDir, "side-B.wav")); err == nil {
		t.Fatal("staged file left behind")
	}
}

func TestSaveAsCopiesStagedSide(t *testing.T) {
	p, _ := Create(t.TempDir(), "p")
	stageTake(t, p, "A", "data")
	q, err := p.SaveAs(context.Background(), t.TempDir(), "N", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(q.Dir(), "side-A.wav")); string(b) != "data" {
		t.Fatalf("%s", b)
	}
	if q.Info().Dirty {
		t.Fatal("new project must be clean")
	}
}

func TestOrphanStagedFileIsLeftover(t *testing.T) {
	dir := t.TempDir()
	p, _ := Create(dir, "p")
	stageTake(t, p, "A", "data")
	q, _ := Open(p.Dir())
	if !q.HasPart("A") || q.SideExists("A") {
		t.Fatal("orphan staged file must be a leftover only")
	}
}

func TestDetailsStageSaveReopen(t *testing.T) {
	dir := t.TempDir()
	p, _ := Create(dir, "p")
	if err := p.SetDetails(" Queen ", true, "1986", " LP ", ""); err != nil {
		t.Fatal(err)
	}
	in := p.Info()
	if !in.Dirty || in.Artist != "Queen" || !in.Compilation || in.Year != "1986" {
		t.Fatalf("%+v", in)
	}
	if q, _ := Open(dir); q.Info().Artist != "" {
		t.Fatal("details written before save")
	}
	if err := p.Save(); err != nil || p.Info().Dirty {
		t.Fatalf("save: %v", err)
	}
	q, _ := Open(dir)
	if i := q.Info(); i.Artist != "Queen" || !i.Compilation || i.Year != "1986" {
		t.Fatalf("artist must be kept while compilation is on: %+v", i)
	}
	_ = q.SetDetails("Queen", true, "1986", "LP", "")
	if q.Info().Dirty {
		t.Fatal("reverting must clear dirty")
	}
	_ = q.SetDetails("X", false, "2000", "", "")
	q.Discard()
	if i := q.Info(); i.Dirty || i.Artist != "Queen" {
		t.Fatalf("discard: %+v", i)
	}
}

func TestDetailsValidation(t *testing.T) {
	p, _ := Create(t.TempDir(), "p")
	for _, y := range []string{"19", "1899", "2101", "abcd", "20001"} {
		if err := p.SetDetails("A", false, y, "", ""); err == nil {
			t.Fatalf("year %q accepted", y)
		}
	}
	if err := p.SetDetails(strings.Repeat("x", 201), false, "", "", ""); err == nil {
		t.Fatal("long artist accepted")
	}
	if err := p.SetDetails("a\nb", false, "", "", ""); err == nil {
		t.Fatal("control chars accepted")
	}
	if p.Info().Dirty {
		t.Fatal("invalid call must change nothing")
	}
}

func TestOpenNormalizesDetailsAndKeepsUnknown(t *testing.T) {
	dir := t.TempDir()
	body := `{"schemaVersion":2,"name":"n","x":1,"artist":"` + strings.Repeat("я", 250) + `\n","year":"19"}`
	_ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(body), 0o644)
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if i := p.Info(); i.Year != "" || len([]rune(i.Artist)) != 200 {
		t.Fatalf("%d %q", len([]rune(i.Artist)), i.Year)
	}
	if err := p.SetDetails(p.Info().Artist, false, "1999", "", ""); err != nil {
		t.Fatalf("unrelated edit must work: %v", err)
	}
	_ = p.Save()
	b, _ := os.ReadFile(filepath.Join(dir, DescriptorName))
	if !strings.Contains(string(b), `"x": 1`) {
		t.Fatal("unknown field lost")
	}
}

func TestSaveFailureRestoresDetails(t *testing.T) {
	p, _ := Create(t.TempDir(), "p")
	_ = p.SetDetails("A", false, "1990", "", "")
	_ = os.Remove(filepath.Join(p.Dir(), DescriptorName))
	_ = os.Chmod(p.Dir(), 0o500)
	defer os.Chmod(p.Dir(), 0o700)
	if err := p.Save(); err == nil {
		t.Skip("directory not read-only for this user")
	}
	if i := p.Info(); i.Artist != "A" || !i.Dirty {
		t.Fatalf("draft must survive: %+v", i)
	}
}

func TestSaveAsCopiesDetails(t *testing.T) {
	p, _ := Create(t.TempDir(), "p")
	_ = p.SetDetails("Abba", false, "1976", "Arrival", "")
	q, err := p.SaveAs(context.Background(), t.TempDir(), "n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if i := q.Info(); i.Artist != "Abba" || i.Year != "1976" || i.Dirty {
		t.Fatalf("%+v", i)
	}
	if r, _ := Open(q.Dir()); r.Info().Artist != "Abba" {
		t.Fatal("not persisted")
	}
}

func TestAlbumDetail(t *testing.T) {
	dir := t.TempDir()
	p, _ := Create(dir, "p")
	if err := p.SetDetails("", true, "", " Arrival ", ""); err != nil {
		t.Fatal(err)
	}
	if i := p.Info(); i.Album != "Arrival" || !i.Compilation || !i.Dirty {
		t.Fatalf("compilation must not touch the album: %+v", i)
	}
	if err := p.SetDetails("", false, "", "a\tb", ""); err == nil {
		t.Fatal("control chars accepted")
	}
	if err := p.SetDetails("", false, "", strings.Repeat("x", 201), ""); err == nil {
		t.Fatal("long album accepted")
	}
	_ = p.Save()
	q, _ := Open(dir)
	if q.Info().Album != "Arrival" {
		t.Fatal("album not persisted")
	}
	_ = q.SetDetails("", false, "", "", "")
	q.Discard()
	if q.Info().Album != "Arrival" {
		t.Fatal("discard must restore album")
	}
	_ = q.SetDetails("", true, "", "Arrival", "")
	if q.Info().Dirty {
		t.Fatal("reverting must clear dirty")
	}
	_ = os.WriteFile(filepath.Join(dir, DescriptorName), []byte(`{"schemaVersion":2,"name":"n","album":"x\u0007`+strings.Repeat("y", 300)+`"}`), 0o644)
	r, _ := Open(dir)
	if l := len([]rune(r.Info().Album)); l != 200 {
		t.Fatalf("open must repair album: %d", l)
	}
}

func TestValidationMessageFollowsLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale(i18n.English) })
	_, err := normalizeDetails(Details{Year: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("got %v", err)
	}
	i18n.SetLocale(i18n.Ukrainian)
	_, err = normalizeDetails(Details{Year: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "рік") {
		t.Fatalf("got %v", err)
	}
}

func TestGenreDetail(t *testing.T) {
	p, err := Create(t.TempDir(), "G")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetDetails("", false, "", "", " Rock "); err != nil {
		t.Fatal(err)
	}
	if i := p.Info(); i.Genre != "Rock" || !i.Dirty {
		t.Fatalf("genre must be staged: %+v", i)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q, err := Open(p.Dir())
	if err != nil || q.Info().Genre != "Rock" || q.Dirty() {
		t.Fatalf("genre must persist: %v %q", err, q.Info().Genre)
	}
	if err := q.SetDetails("", false, "", "", strings.Repeat("g", 101)); err == nil {
		t.Fatal("genre over 100 characters must be rejected")
	}
}

func TestSaveDropsRemovedTapeKeys(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "p"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DescriptorName)
	b, _ := os.ReadFile(path)
	b = bytes.Replace(b, []byte("{"), []byte(`{"notes": "old", "tapeBrand": "TDK", "tapeType": "II", "noiseReduction": "dolbyB", "keep": 1,`), 1)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	for _, k := range []string{"notes", "tapeBrand", "tapeType", "noiseReduction"} {
		if bytes.Contains(b, []byte(`"`+k+`"`)) {
			t.Fatalf("%s kept: %s", k, b)
		}
	}
	if !bytes.Contains(b, []byte(`"keep"`)) {
		t.Fatal("unknown keys must be kept")
	}
}
