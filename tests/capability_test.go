package waxlabel_test

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/core"
)

// TestCodecCapabilitiesNilSafe proves every registered codec answers a file-agnostic
// capability query (m == nil, as PlanTransfer makes) without panicking and self-reports the
// format it claims. The file-uniform codecs ignore the *core.Media and Matroska nil-guards
// before reading docType, so a nil file must be safe for all of them.
func TestCodecCapabilitiesNilSafe(t *testing.T) {
	codecs := core.Codecs()
	if len(codecs) == 0 {
		t.Fatal("no codecs registered (the package import side effect is missing)")
	}
	for _, c := range codecs {
		caps := c.Capabilities(nil, core.DefaultWriteOptions())
		if caps.Format != c.Format() {
			t.Errorf("%s: caps.Format = %s, want the codec's own format", c.Format(), caps.Format)
		}
	}
}

// These bound the machine-generated capability table in the README, rendered from the codec
// Capabilities so its per-format picture and chapter facts cannot drift from the code.
// TestReadmeCapabilityBlockDerived regenerates the block and asserts the committed README
// carries it verbatim; on a capability change, run the test and paste the block from its
// failure output between the markers.
const (
	capsBlockBegin = "<!-- BEGIN caps (generated from codec Capabilities; see tests/capability_test.go) -->"
	capsBlockEnd   = "<!-- END caps -->"
)

// renderCapabilityBlock renders the per-format picture/chapter/synced-lyrics capability
// table from the public CapabilitiesFor query. Formats are sorted by name so the block is
// stable regardless of codec registration order.
func renderCapabilityBlock() string {
	formats := wl.Formats()
	sort.Slice(formats, func(i, j int) bool { return formats[i].String() < formats[j].String() })
	var b strings.Builder
	b.WriteString("| Format | Pictures | Chapters | Synced Lyrics |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, f := range formats {
		caps := wl.CapabilitiesFor(f)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", f, capCell(caps.Pictures), capCell(caps.Chapters), capCell(caps.SyncedLyrics))
	}
	return b.String()
}

// capCell renders one capability dimension as "read X, write Y · representation",
// mirroring the caps command's own row format.
func capCell(c wl.Capability) string {
	s := fmt.Sprintf("read %s, write %s", c.Read, c.Write)
	if c.Representation != "" {
		s += " · " + c.Representation
	}
	return s
}

// TestReadmeCapabilityBlockDerived renders the capability block from the codecs and asserts
// the committed README carries it verbatim between the markers, so its caps facts are
// generated rather than hand-maintained.
func TestReadmeCapabilityBlockDerived(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	want := renderCapabilityBlock()
	got, ok := extractBetween(string(readme), capsBlockBegin, capsBlockEnd)
	if !ok {
		t.Fatalf("README is missing the caps markers (%q … %q); insert this block between them:\n\n%s",
			capsBlockBegin, capsBlockEnd, want)
	}
	gotLF, wantLF := normalizeEOL(got), normalizeEOL(want)
	if strings.TrimSpace(gotLF) != strings.TrimSpace(wantLF) {
		t.Errorf("README caps block is stale; replace the content between the markers with:\n\n%s\n\ngot:\n\n%s", want, gotLF)
	}
}

// normalizeEOL rewrites CRLF to LF. The README is read from the working tree, whose line
// endings belong to whoever cloned the repo (Git for Windows defaults to core.autocrlf=true),
// while the want block is built with \n. Comparing raw would measure that checkout policy
// instead of capability drift. The repo's .gitattributes pins LF, so this is the second line
// of defense rather than the only one.
func normalizeEOL(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// extractBetween returns the text strictly between the first begin and the next end
// marker, and whether both were found in order.
func extractBetween(s, begin, end string) (string, bool) {
	i := strings.Index(s, begin)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
