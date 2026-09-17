package main

import (
	"strings"
	"testing"
)

// TestDumpJSONRoundsMilliseconds: chapter start 362.81 ms rounds to nearest ms in JSON.
func TestDumpJSONRoundsMilliseconds(t *testing.T) {
	t.Parallel()
	jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, td("chapters.mpc")))
	if len(jd.Chapters) < 4 || jd.Chapters[2].StartMs != 363 || jd.Chapters[3].StartMs != 431 {
		t.Errorf("chapters = %+v, want starts 363 and 431", jd.Chapters)
	}
	out, _, _ := runCLI(t, "dump", td("chapters.mpc"))
	if !strings.Contains(out, "0:00:00.363") {
		t.Errorf("human listing should round too:\n%s", out)
	}
}
