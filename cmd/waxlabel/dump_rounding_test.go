package main

import (
	"strings"
	"testing"
)

// TestDumpJSONRoundsMilliseconds: chapters.mpc's third chapter starts at sample 16000 of a
// 44100 Hz stream (362.81 ms); the JSON reports the nearest millisecond, not the floor.
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
