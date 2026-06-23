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

// TestCodecCapabilitiesNilSafe proves every registered codec answers a
// file-agnostic capability query (m == nil, as PlanTransfer makes) without
// panicking and self-reports the format it claims. Step 14 threaded a *core.Media
// into Capabilities; the 8 file-uniform codecs ignore it and Matroska nil-guards
// before reading docType, so a nil file must be safe for all of them. The named
// import below ensures registration regardless of how this file is compiled, so the
// test does not silently depend on a sibling importing the package.
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

// capsBlockMarkers bound the machine-generated capability table in the README. The
// table is rendered from the codec Capabilities (the same structured source `caps`
// renders), so the README's per-format picture and chapter facts cannot silently
// drift from the code (the caps-vs-README class - F5's Matroska chapters and P2's m4a
// cover types). TestReadmeCapabilityBlockDerived regenerates the block and asserts the
// committed README carries it verbatim; on a capability change, run the test to see the
// new block in the failure output and paste it between the markers.
const (
	capsBlockBegin = "<!-- BEGIN caps (generated from codec Capabilities; see tests/capability_test.go) -->"
	capsBlockEnd   = "<!-- END caps -->"
)

// renderCapabilityBlock renders the per-format picture/chapter capability table from
// the public CapabilitiesFor query. Formats are sorted by name so the block is stable
// regardless of codec registration order.
func renderCapabilityBlock() string {
	formats := wl.Formats()
	sort.Slice(formats, func(i, j int) bool { return formats[i].String() < formats[j].String() })
	var b strings.Builder
	b.WriteString("| Format | Pictures | Chapters |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, f := range formats {
		caps := wl.CapabilitiesFor(f)
		fmt.Fprintf(&b, "| %s | %s | %s |\n", f, capCell(caps.Pictures), capCell(caps.Chapters))
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

// TestReadmeCapabilityBlockDerived (Prevention) renders the capability block from the
// codecs and asserts the committed README carries it verbatim between the markers, so
// the README's caps facts are literally generated, not hand-maintained - closing the
// caps-vs-README drift class (F5/P2).
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
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Errorf("README caps block is stale; replace the content between the markers with:\n\n%s\n\ngot:\n\n%s", want, got)
	}
}

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
