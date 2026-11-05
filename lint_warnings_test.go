package waxlabel

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestLintWarningsPassThroughLayoutCodes: preserved-but-unread regions surface as lint
// warnings; a multiplexed stream is legitimate, only unwritable, so it is info.
func TestLintWarningsPassThroughLayoutCodes(t *testing.T) {
	for code, want := range map[core.WarningCode]LintSeverity{
		core.WarnTrailingLyrics3:   LintWarning,
		core.WarnLeadingBytes:      LintWarning,
		core.WarnPageGap:           LintWarning,
		core.WarnMultiplexedStream: LintInfo,
	} {
		fs := lintWarnings([]core.Warning{{Code: code, Message: "m"}})
		if len(fs) != 1 {
			t.Errorf("%v: %d findings, want 1", code, len(fs))
			continue
		}
		if fs[0].Code != code.String() || fs[0].Severity != want || fs[0].Message != "m" {
			t.Errorf("%v: finding = %+v, want code %q at %v", code, fs[0], code.String(), want)
		}
	}
}
