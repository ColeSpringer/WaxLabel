package core

import "testing"

// TestWarningCodeStrings pins the machine codes (JSON and lint surface them) and checks the codes
// with a String case are one unbroken run, each with its own string.
func TestWarningCodeStrings(t *testing.T) {
	for code, want := range map[WarningCode]string{
		WarnTrailingLyrics3:   "trailing-lyrics3",
		WarnLeadingBytes:      "leading-bytes",
		WarnPageGap:           "page-gap",
		WarnMultiplexedStream: "multiplexed-stream",
	} {
		if got := code.String(); got != want {
			t.Errorf("WarningCode(%d).String() = %q, want %q", code, got, want)
		}
	}
	seen := map[string]WarningCode{}
	last := WarnUnknown
	for c := WarnUnknown + 1; c != WarnUnknown; c++ { // every value of the uint8, wrapping to 0
		s := c.String()
		if s == "unknown" {
			continue
		}
		if c != last+1 {
			t.Errorf("WarningCode(%d) has no String case but WarningCode(%d) does", last+1, c)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("WarningCode(%d) and WarningCode(%d) both render %q", prev, c, s)
		}
		seen[s], last = c, c
	}
}
