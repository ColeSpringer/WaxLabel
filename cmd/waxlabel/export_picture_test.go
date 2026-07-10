package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExportPictureRoundTrip: exporting an embedded cover writes its image bytes verbatim, so
// the output file is byte-identical to the source image, and the JSON reports the picture.
func TestExportPictureRoundTrip(t *testing.T) {
	t.Parallel()
	cover := writeTempImage(t, "cover.png", minimalPNG())
	f := copyFixture(t, notagsFLAC)
	if _, errb, code := runCLI(t, "set", f, "--add-cover", cover); code != 0 {
		t.Fatalf("authoring the cover exit %d: %s", code, errb)
	}

	outPath := filepath.Join(t.TempDir(), "extracted.png")
	out, errb, code := runCLI(t, "--json", "export-picture", f, "-o", outPath)
	if code != 0 {
		t.Fatalf("export-picture exit %d: %s", code, errb)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if !bytes.Equal(got, minimalPNG()) {
		t.Errorf("exported bytes are not byte-identical to the source image (got %d bytes, want %d)", len(got), len(minimalPNG()))
	}
	var jp jsonExportPicture // export-picture emits a single object, not the array plan/set do
	if err := json.Unmarshal([]byte(out), &jp); err != nil {
		t.Fatalf("export JSON: %v\n%s", err, out)
	}
	if jp.Picture.MIME != "image/png" || jp.Picture.Bytes != len(minimalPNG()) || jp.Output != outPath {
		t.Errorf("export JSON = %+v, want image/png / %d bytes / output %q", jp, len(minimalPNG()), outPath)
	}
}

// TestExportPictureSelectorErrors covers the exactly-one resolver's failure modes: an explicit
// role that matches nothing, an ambiguous role that matches several (unlike --remove-picture, a
// no-match or multi-match is an error here), an out-of-range index, a file with no pictures, and
// the no-selector default when there is no single front cover to pick.
func TestExportPictureSelectorErrors(t *testing.T) {
	t.Parallel()
	pngA := writeTempImage(t, "a.png", minimalPNG())
	pngB := writeTempImage(t, "b.png", append(minimalPNG(), 0x00)) // distinct bytes, same role
	f := copyFixture(t, notagsFLAC)
	// Two front covers, so a role selector is ambiguous and the no-selector default cannot pick.
	if _, errb, code := runCLI(t, "set", f, "--add-picture", "front-cover="+pngA, "--add-picture", "front-cover="+pngB); code != 0 {
		t.Fatalf("authoring two covers exit %d: %s", code, errb)
	}
	out := filepath.Join(t.TempDir(), "out.png")

	cases := []struct {
		name string
		args []string
	}{
		{"unknown role", []string{"--picture", "back-cover"}},
		{"ambiguous role", []string{"--picture", "front-cover"}},
		{"out-of-range index", []string{"--picture", "9"}},
		{"no single front cover default", nil},
	}
	for _, c := range cases {
		args := append([]string{"export-picture", f, "-o", out}, c.args...)
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%s: exit = %d, want 2 (usage error)", c.name, code)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: wrote an output file despite the selector error", c.name)
			_ = os.Remove(out)
		}
	}

	// An explicit index still picks exactly one of the two covers.
	if _, errb, code := runCLI(t, "export-picture", f, "--picture", "2", "-o", out); code != 0 {
		t.Fatalf("index selector exit %d: %s", code, errb)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(minimalPNG(), 0x00)) {
		t.Errorf("--picture 2 exported the wrong cover")
	}

	// A file with no pictures is a usage error, not a silent empty file.
	none := copyFixture(t, notagsFLAC)
	if _, _, code := runCLI(t, "export-picture", none, "-o", filepath.Join(t.TempDir(), "none.png")); code != 2 {
		t.Errorf("no-pictures export exit = %d, want 2", code)
	}
}

// TestExportPictureRefusesInputAsOutput guards the "input is never modified" invariant: an -o
// that resolves to the input (the same path, a symlink to it, or a hardlink sharing its inode)
// is refused, so the audio is never clobbered with the extracted cover bytes. The refusal is
// unconditional - even --overwrite cannot make in-place extraction meaningful - unlike set,
// whose -o legitimately targets the input for an atomic in-place rewrite.
func TestExportPictureRefusesInputAsOutput(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleMKA) // carries a cover
	orig, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	intact := func(when string) {
		t.Helper()
		got, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if !bytes.Equal(got, orig) {
			t.Fatalf("%s: the input audio was modified (%d bytes, want the original %d)", when, len(got), len(orig))
		}
	}

	// -o naming the input directly: refused without --overwrite, and still refused with it.
	if _, _, code := runCLI(t, "export-picture", f, "-o", f); code != 2 {
		t.Errorf("-o == input exit = %d, want 2 (refused)", code)
	}
	intact("after -o == input")
	if _, _, code := runCLI(t, "export-picture", f, "-o", f, "--overwrite"); code != 2 {
		t.Errorf("-o == input with --overwrite exit = %d, want 2 (in-place extraction is never valid)", code)
	}
	intact("after -o == input --overwrite")

	// A symlink to the input resolves to the same file, so it is refused too.
	link := filepath.Join(t.TempDir(), "link.mka")
	if err := os.Symlink(f, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	if _, _, code := runCLI(t, "export-picture", f, "-o", link); code != 2 {
		t.Errorf("-o symlink-to-input exit = %d, want 2 (refused)", code)
	}
	intact("after -o symlink-to-input")
}

// TestExportPictureOverwriteGate: an existing output file is refused unless --overwrite, so a
// stray export does not clobber a file, while --overwrite replaces it.
func TestExportPictureOverwriteGate(t *testing.T) {
	t.Parallel()
	cover := writeTempImage(t, "cover.png", minimalPNG())
	f := copyFixture(t, notagsFLAC)
	if _, errb, code := runCLI(t, "set", f, "--add-cover", cover); code != 0 {
		t.Fatalf("authoring exit %d: %s", code, errb)
	}
	out := filepath.Join(t.TempDir(), "cover-out.png")
	if err := os.WriteFile(out, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, code := runCLI(t, "export-picture", f, "-o", out); code != 2 {
		t.Errorf("export over an existing file should refuse (exit 2), got %d", code)
	}
	if got, _ := os.ReadFile(out); string(got) != "pre-existing" {
		t.Errorf("the existing file was modified without --overwrite")
	}
	if _, _, code := runCLI(t, "export-picture", f, "-o", out, "--overwrite"); code != 0 {
		t.Errorf("--overwrite should replace the existing file, got exit %d", code)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, minimalPNG()) {
		t.Errorf("--overwrite did not write the cover bytes")
	}
}
