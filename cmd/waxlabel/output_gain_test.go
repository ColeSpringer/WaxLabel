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
