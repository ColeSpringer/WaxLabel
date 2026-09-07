package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCLISetOutputGainOpus walks the whole flag: the plan shows the change, set writes it,
// dump reports it in both renderings, and verify's digest is unchanged because the gain is
// masked out of the hashed configuration.
func TestCLISetOutputGainOpus(t *testing.T) {
	path := copyFixture(t, td("sample.opus"))
	digestBefore, _, code := runCLI(t, "verify", path)
	if code != 0 {
		t.Fatalf("verify exit = %d", code)
	}

	stdout, stderr, code := runCLI(t, "plan", path, "--output-gain", "-3.5")
	if code != 0 {
		t.Fatalf("plan exit = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "~ output gain: 0.00 dB -> -3.50 dB") {
		t.Errorf("plan output missing the gain change line:\n%s", stdout)
	}
	jsonPlan, _, code := runCLI(t, "--json", "plan", path, "--output-gain", "-3.5")
	if code != 0 {
		t.Fatalf("plan --json exit = %d", code)
	}
	if !strings.Contains(jsonPlan, `"output gain"`) || !strings.Contains(jsonPlan, "-3.50 dB") {
		t.Errorf("plan --json missing the gain change:\n%s", jsonPlan)
	}

	if _, stderr, code := runCLI(t, "set", path, "--output-gain", "-3.5"); code != 0 {
		t.Fatalf("set exit = %d; stderr=%q", code, stderr)
	}

	jd := dumpJSON(t, path)
	if jd.Properties == nil || jd.Properties.OutputGainDb != -3.5 {
		t.Errorf("dump --json outputGainDb = %v, want -3.5", jd.Properties)
	}
	text, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(text, "gain -3.50 dB") {
		t.Errorf("text dump missing the gain:\n%s", text)
	}
	native, _, code := runCLI(t, "dump", path, "--native")
	if code != 0 {
		t.Fatalf("dump --native exit = %d", code)
	}
	if !strings.Contains(native, "output gain -3.50 dB") {
		t.Errorf("native dump missing the OpusHead note:\n%s", native)
	}

	digestAfter, _, code := runCLI(t, "verify", path)
	if code != 0 {
		t.Fatalf("verify exit = %d", code)
	}
	if digestBefore != digestAfter {
		t.Errorf("verify digest changed with the gain:\n%s\n%s", digestBefore, digestAfter)
	}
	if !strings.Contains(digestAfter, "ogg-opus-packets-v2") {
		t.Errorf("verify output should name the v2 extent:\n%s", digestAfter)
	}
}

// TestCLIOutputGainUnsupportedDroppedAndStrict: a format that stores no gain drops it with
// a warning, exits 0, and still applies the rest of the edit; --strict refuses.
func TestCLIOutputGainUnsupportedDroppedAndStrict(t *testing.T) {
	path := copyFixture(t, td("sample.mp3"))
	stdout, _, code := runCLI(t, "set", path, "--output-gain", "-3.5", "--set", "TITLE=Kept")
	if code != 0 {
		t.Fatalf("set exit = %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "output-gain-unsupported") {
		t.Errorf("expected an output-gain-unsupported warning:\n%s", stdout)
	}
	if got := dumpJSON(t, path); tagValues(got, "TITLE") == nil || tagValues(got, "TITLE")[0] != "Kept" {
		t.Errorf("TITLE = %v, want it written despite the dropped gain", tagValues(got, "TITLE"))
	}

	if _, _, code := runCLI(t, "set", copyFixture(t, td("sample.mp3")), "--output-gain", "-3.5", "--strict"); code != 2 {
		t.Errorf("set --strict exit = %d, want 2", code)
	}
}

// TestCLIOutputGainReadOnlyExits3: a format that cannot be written at all is an
// unsupported-format failure, never a silent success.
func TestCLIOutputGainReadOnlyExits3(t *testing.T) {
	if _, _, code := runCLI(t, "set", copyFixture(t, td("sample.wma")), "--output-gain", "-3.5"); code != 3 {
		t.Errorf("set exit = %d, want 3 (unsupported format)", code)
	}
}

// TestCLIOutputGainUsageErrors: the flag takes decibels, so anything that is not a finite
// in-range number is a usage error.
func TestCLIOutputGainUsageErrors(t *testing.T) {
	for _, v := range []string{"abc", "200", "-200", "nan", "inf", "1e400"} {
		if _, _, code := runCLI(t, "set", copyFixture(t, td("sample.opus")), "--output-gain", v); code != 2 {
			t.Errorf("--output-gain %q exit = %d, want 2", v, code)
		}
	}
}

// TestCLIOutputGainRoundsToQ78: the stored field is Q7.8, so a finer dB value rounds to
// the nearest step.
func TestCLIOutputGainRoundsToQ78(t *testing.T) {
	path := copyFixture(t, td("sample.opus"))
	if _, stderr, code := runCLI(t, "set", path, "--output-gain", "-3.501"); code != 0 {
		t.Fatalf("set exit = %d; stderr=%q", code, stderr)
	}
	if got := dumpJSON(t, path); got.Properties.OutputGainDb != -3.5 {
		t.Errorf("outputGainDb = %v, want -3.5", got.Properties.OutputGainDb)
	}
}

// capLine reports whether the caps report has a row with the given label and value.
func capLine(t *testing.T, out, label, want string) bool {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		f := strings.TrimSpace(line)
		if strings.HasPrefix(f, label) {
			return strings.TrimSpace(strings.TrimPrefix(f, label)) == want
		}
	}
	return false
}

// TestCapsOutputGainLevel: caps reports the dimension per format.
func TestCapsOutputGainLevel(t *testing.T) {
	for _, c := range []struct{ format, want string }{
		{"opus", "full"},
		{"flac", "none"},
	} {
		out, _, code := runCLI(t, "caps", "--format", c.format)
		if code != 0 {
			t.Fatalf("caps --format %s exit = %d", c.format, code)
		}
		if !capLine(t, out, "output gain:", c.want) {
			t.Errorf("caps --format %s should report output gain %s:\n%s", c.format, c.want, out)
		}
	}
}

// TestCLIDiffSeesOutputGain: set --output-gain changes the file, so diff must not call the
// result identical to the original.
func TestCLIDiffSeesOutputGain(t *testing.T) {
	orig := copyFixture(t, td("sample.opus"))
	edited := copyFixture(t, td("sample.opus"))
	if _, stderr, code := runCLI(t, "set", edited, "--output-gain", "-3.5"); code != 0 {
		t.Fatalf("set exit = %d; stderr=%q", code, stderr)
	}

	stdout, _, code := runCLI(t, "diff", orig, edited)
	if code != 1 {
		t.Errorf("diff exit = %d, want 1 (the files differ)", code)
	}
	if !strings.Contains(stdout, "output gain: 0.00 dB -> -3.50 dB") {
		t.Errorf("diff should name the gain delta:\n%s", stdout)
	}

	out, _, _ := runCLI(t, "--json", "diff", orig, edited)
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("decode diff --json: %v\n%s", err, out)
	}
	if jd.Identical {
		t.Error("diff --json reported identical metadata for files differing in header gain")
	}
	if jd.OutputGain == nil || jd.OutputGain.A != "0.00 dB" || jd.OutputGain.B != "-3.50 dB" {
		t.Errorf("diff --json outputGain = %+v, want 0.00 dB -> -3.50 dB", jd.OutputGain)
	}

	// A format with no header gain reports none at all.
	same, _, code := runCLI(t, "--json", "diff", td("sample.mp3"), td("sample.mp3"))
	if code != 0 {
		t.Fatalf("diff of a file with itself exit = %d", code)
	}
	var sameJD jsonDiff
	if err := json.Unmarshal([]byte(same), &sameJD); err != nil {
		t.Fatalf("decode diff --json: %v\n%s", err, same)
	}
	if sameJD.OutputGain != nil {
		t.Error("outputGain should be absent when neither file carries one")
	}
}

// r128Fixture copies sample.opus and gives it the two R128 loudness tags.
func r128Fixture(t *testing.T, track, album string) string {
	t.Helper()
	path := copyFixture(t, td("sample.opus"))
	args := []string{"set", path, "--set", "R128_TRACK_GAIN=" + track}
	if album != "" {
		args = append(args, "--set", "R128_ALBUM_GAIN="+album)
	}
	if out, errb, code := runCLI(t, args...); code != 0 {
		t.Fatalf("writing the R128 tags: exit = %d\n%s\n%s", code, out, errb)
	}
	return path
}

// TestCLIOutputGainRebasesR128: RFC 7845 applies the R128 tags on top of the header gain, so
// the plan shows them moving with it and the write is clean even under --strict.
func TestCLIOutputGainRebasesR128(t *testing.T) {
	t.Parallel()
	path := r128Fixture(t, "-896", "-512")
	stdout, stderr, code := runCLI(t, "plan", path, "--output-gain", "-3.5")
	if code != 0 {
		t.Fatalf("plan exit = %d; stderr=%q", code, stderr)
	}
	for _, want := range []string{"~ R128_TRACK_GAIN: -896 -> 0", "~ R128_ALBUM_GAIN: -512 -> 384"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan output missing %q:\n%s", want, stdout)
		}
	}

	out, _, code := runCLI(t, "set", path, "--output-gain", "-3.5", "--strict")
	if code != 0 {
		t.Fatalf("set --strict exit = %d:\n%s", code, out)
	}
	if strings.Contains(out, "output-gain-r128-tags") {
		t.Errorf("a rebase leaves nothing to advise about:\n%s", out)
	}
	jd := dumpJSON(t, path)
	if got := tagValues(jd, "R128_TRACK_GAIN"); len(got) != 1 || got[0] != "0" {
		t.Errorf("R128_TRACK_GAIN = %v, want [0]", got)
	}
	if got := tagValues(jd, "R128_ALBUM_GAIN"); len(got) != 1 || got[0] != "384" {
		t.Errorf("R128_ALBUM_GAIN = %v, want [384]", got)
	}
}

// TestCLIOutputGainR128ExplicitSetWins: --output-gain's own help text tells the user to set
// the tag, so that must not be refused as an unknown key, and it must beat the rebase.
func TestCLIOutputGainR128ExplicitSetWins(t *testing.T) {
	t.Parallel()
	path := r128Fixture(t, "-896", "")
	out, errb, code := runCLI(t, "set", path, "--output-gain", "-3.5", "--set", "R128_TRACK_GAIN=0", "--strict")
	if code != 0 {
		t.Fatalf("set --strict exit = %d:\n%s\n%s", code, out, errb)
	}
	if got := tagValues(dumpJSON(t, path), "R128_TRACK_GAIN"); len(got) != 1 || got[0] != "0" {
		t.Errorf("R128_TRACK_GAIN = %v, want the explicit [0]", got)
	}
}

// TestCLIKeepR128: the opt-out leaves both tags alone, says so, and stays advisory even
// under --strict. Without --output-gain there is nothing to opt out of.
func TestCLIKeepR128(t *testing.T) {
	t.Parallel()
	path := r128Fixture(t, "-896", "-512")
	out, _, code := runCLI(t, "set", path, "--output-gain", "-3.5", "--keep-r128", "--strict")
	if code != 0 {
		t.Fatalf("set --keep-r128 --strict exit = %d:\n%s", code, out)
	}
	if !strings.Contains(out, "output-gain-r128-tags") {
		t.Errorf("expected the kept-tag advisory:\n%s", out)
	}
	jd := dumpJSON(t, path)
	if got := tagValues(jd, "R128_TRACK_GAIN"); len(got) != 1 || got[0] != "-896" {
		t.Errorf("R128_TRACK_GAIN = %v, want the kept [-896]", got)
	}
	if jd.Properties == nil || jd.Properties.OutputGainDb != -3.5 {
		t.Errorf("the gain edit should still have applied; got %v", jd.Properties)
	}

	if _, _, code := runCLI(t, "set", copyFixture(t, td("sample.opus")), "--keep-r128"); code != 2 {
		t.Errorf("--keep-r128 without --output-gain exit = %d, want 2", code)
	}
}

// TestCLIOutputGainR128Malformed: a value that is not a Q7.8 integer is noted when set and
// cannot be rebased later, so a gain edit advises rather than silently leaving it wrong.
func TestCLIOutputGainR128Malformed(t *testing.T) {
	t.Parallel()
	path := copyFixture(t, td("sample.opus"))
	out, errb, code := runCLI(t, "set", path, "--set", "R128_TRACK_GAIN=abc")
	if code != 0 {
		t.Fatalf("set exit = %d:\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(out+errb, "does not look like an R128 gain") {
		t.Errorf("expected the value note:\n%s\n%s", out, errb)
	}
	plan, _, code := runCLI(t, "plan", path, "--output-gain", "-3.5")
	if code != 0 {
		t.Fatalf("plan exit = %d:\n%s", code, plan)
	}
	if !strings.Contains(plan, "output-gain-r128-tags") {
		t.Errorf("expected the unrebasable-tag advisory:\n%s", plan)
	}
}

// TestCLILintR128: the RFC defines these keys, so lint checks their values and does not
// call them custom fields.
func TestCLILintR128(t *testing.T) {
	t.Parallel()
	bad := copyFixture(t, td("sample.opus"))
	if out, _, code := runCLI(t, "set", bad, "--set", "R128_TRACK_GAIN=abc"); code != 0 {
		t.Fatalf("set exit = %d:\n%s", code, out)
	}
	out, _, code := runCLI(t, "--json", "lint", bad)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1 (findings present)\n%s", code, out)
	}
	found := false
	for _, f := range decodeJSONList[jsonLint](t, out)[0].Findings {
		if f.Key == "R128_TRACK_GAIN" {
			if f.Code == "custom-key" {
				t.Errorf("R128 keys are RFC-defined, not custom fields:\n%s", out)
			}
			if f.Code == "malformed-number" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("lint did not flag the malformed R128 value:\n%s", out)
	}

	// sample.opus carries ffmpeg's encoder stamps, so it is never finding-free; what matters
	// is that valid R128 values contribute nothing.
	good := r128Fixture(t, "-896", "-512")
	out, _, code = runCLI(t, "--json", "lint", good)
	if code > 1 {
		t.Fatalf("lint exit = %d\n%s", code, out)
	}
	for _, f := range decodeJSONList[jsonLint](t, out)[0].Findings {
		if f.Key == "R128_TRACK_GAIN" || f.Key == "R128_ALBUM_GAIN" {
			t.Errorf("valid R128 values should draw no finding; got %s on %s:\n%s", f.Code, f.Key, out)
		}
	}
}
