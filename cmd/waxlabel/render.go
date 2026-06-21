package main

import (
	"fmt"
	"io"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// displayName maps a path to its display form for a text record header: the "-"
// stdin sentinel reads as "<stdin>", and every real path is shown through
// [tag.SanitizeLine]. A Linux filename may contain any byte but '/' and NUL, so a
// name handed over by a shell glob or the --recursive walk could otherwise forge a
// line or drive the terminal from a record header before a byte is parsed; the CLI
// boundary already blocks the control-hijack, and escaping the newline here also
// stops line-forgery in a multi-file listing. It is otherwise cosmetic - readInputs
// already keeps the buffered temp path out of the renderers (they receive the
// original "-" arg, never the temp path). JSON output keeps the raw path/"-" so a
// script still keys on the argument it passed.
func displayName(path string) string {
	if path == stdinArg {
		return "<stdin>"
	}
	return tag.SanitizeLine(path)
}

// renderDocument writes the human-readable view of a parsed file.
func renderDocument(w io.Writer, path string, doc *wl.Document, native bool) {
	fmt.Fprintf(w, "%s\n", displayName(path))
	fmt.Fprintf(w, "  format:  %s\n", doc.Format())
	if line := audioLine(doc.Properties()); line != "" {
		fmt.Fprintf(w, "  audio:   %s\n", line)
	}
	renderTags(w, doc.Tags())
	renderPictures(w, doc.Pictures())
	renderChapters(w, doc.Chapters())
	renderWarnings(w, doc.Warnings())
	if native {
		renderNative(w, doc)
	}
}

// audioLine summarizes the first audio track as a single comma-separated line,
// skipping fields that are not populated.
func audioLine(p wl.Properties) string {
	t := p.First()
	var parts []string
	// hasSubstantive tracks whether the line carries information worth showing. A
	// bare codec name with no technical detail (the 10-byte empty.mp3 that shows
	// "MPEG AUDIO" beside a [no-audio] warning) is degenerate and drops the line; a
	// genuine decodable stream always reports a sample rate and duration, so this
	// never hides a real stream.
	hasSubstantive := false
	switch {
	case t.Codec != "":
		// t.Codec is the canonical name (CanonicalCodec, applied at parse), but for an
		// unrecognized container codec ID it is the file's raw bytes (e.g. Matroska's
		// codecName returns the ID tail verbatim), so escape it for the single-line row.
		parts = append(parts, tag.SanitizeLine(strings.ToUpper(t.Codec)))
	case p.Container != "":
		// No codec was identified (e.g. an unrecognized Matroska/MP4 track): name the
		// container and say so, rather than printing the container as if it were the
		// codec (a bare "MATROSKA"). This "codec unknown" form is itself an
		// informative signal (the container parsed; the codec did not resolve), so
		// keep the line even when no technical properties accompany it - unlike a bare
		// codec name, which would just be noise. The container is a fixed label today,
		// but escape it for the single-line row in case a format ever derives it from
		// the file.
		parts = append(parts, fmt.Sprintf("%s (codec unknown)", tag.SanitizeLine(p.Container)))
		hasSubstantive = true
	}
	if t.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%d Hz", t.SampleRate))
		hasSubstantive = true
	}
	if t.Channels > 0 {
		parts = append(parts, fmt.Sprintf("%d ch", t.Channels))
		hasSubstantive = true
	}
	// Bit depth describes the stored samples only for codecs that have a fixed sample
	// width; a lossy codec decodes to PCM at an arbitrary depth, so a stored "16-bit"
	// there (e.g. the legacy 16 MP4 writes for AAC) is noise. Show it only when the
	// codec carries a real depth.
	if t.BitsPerSample > 0 && bitDepthMeaningful(t.Codec) {
		parts = append(parts, fmt.Sprintf("%d-bit", t.BitsPerSample))
		hasSubstantive = true
	}
	if d := p.Duration(); d > 0 {
		parts = append(parts, humanDuration(d))
		hasSubstantive = true
	}
	// Round to kbps; a sub-1-kbps average (a truncated file's collapsed bitrate)
	// would print as a misleading "0 kbps", so it is omitted instead. Gate on a
	// non-zero duration too: a header-only file (empty.wav, zero samples) has a
	// header-derived rate×ch×depth bitrate that is meaningless over zero playtime -
	// an average bitrate is undefined there - so the truthful header facts (codec,
	// rate, channels, depth) stay while the bogus "705 kbps" is dropped.
	if t.Bitrate >= 1000 && p.Duration() > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", t.Bitrate/1000))
		hasSubstantive = true
	}
	if !hasSubstantive {
		return ""
	}
	return strings.Join(parts, ", ")
}

// bitDepthMeaningful reports whether codec stores samples at a fixed width, the
// only case where a "bits per sample" figure describes the audio. It excludes the
// lossy/perceptual codecs (AAC, MP1/2/3, Opus, Vorbis, AC-3, E-AC-3, Musepack), which
// decode to PCM at the decoder's chosen depth so a container-stored depth (often a legacy
// default like the 16 MP4 writes for AAC) is meaningless. Inverting the test this
// way - a blacklist of the small, stable lossy set rather than a whitelist of the
// open-ended lossless one - keeps a real depth for the long tail of PCM-family and
// lossless codecs (A-law/mu-law, ADPCM, FLAC, ALAC, WavPack, TTA, MLP, ...) that the
// parsers do report a depth for. codec is the canonical name (CanonicalCodec, so
// AAC's object-type spellings already collapsed to "AAC"). DTS is deliberately not
// excluded: its DTS-HD Master Audio variant is lossless and shares the same name, so
// suppressing it would drop a real depth.
func bitDepthMeaningful(codec string) bool {
	switch strings.ToUpper(codec) {
	case "AAC", "MP1", "MP2", "MP3", "OPUS", "VORBIS", "AC-3", "E-AC-3", "MPC":
		return false
	}
	return true
}

// keyColumn caps the alignment width so one very long key does not push every
// value far to the right.
const keyColumn = 24

// renderTags prints the canonical tag set, one value per line with the key
// repeated for multi-valued fields, preserving the set's order.
func renderTags(w io.Writer, ts tag.TagSet) {
	if ts.Len() == 0 {
		fmt.Fprintln(w, "  tags:    (none)")
		return
	}
	// "key"/"keys" makes explicit that the count is of distinct keys, not values:
	// the layout prints one value per line (the key repeated for a multi-valued
	// field), matching the JSON shape (keyed by distinct key, values under "values").
	n := ts.Len()
	keyWord := "keys"
	if n == 1 {
		keyWord = "key"
	}
	fmt.Fprintf(w, "  tags (%d %s):\n", n, keyWord)
	width := 0
	for _, k := range ts.Keys() {
		if n := len(k); n > width {
			width = n
		}
	}
	if width > keyColumn {
		width = keyColumn
	}
	// Values print in the column after the key (4-space indent + key width + 2);
	// continuation lines of a multi-line value (lyrics, comments) align there too.
	valueCol := 4 + width + 2
	for k, vals := range ts.All() {
		// A key is validated printable ASCII, but sanitize defensively so a hostile
		// key from a malformed file cannot inject control bytes - or, via an embedded
		// newline, forge a fake tag line - into the listing. SanitizeLine (not
		// SanitizeText) because a key is single-line: it also escapes \n/\t.
		ks := tag.SanitizeLine(string(k))
		if len(vals) == 0 {
			fmt.Fprintf(w, "    %-*s  (present, no value)\n", width, ks)
			continue
		}
		// A known single-valued key holding several values is the
		// [conflicting-families] merge surfacing as duplicate rows; flag each with the
		// same "(conflict)" marker the per-source view uses, so the rows visibly tie
		// back to the warning rather than reading as an unexplained repeat (L5).
		suffix := ""
		if k.SingleValuedMulti(len(vals)) {
			suffix = "  (conflict)"
		}
		for _, v := range vals {
			fmt.Fprintf(w, "    %-*s  ", width, ks)
			// A present-but-empty value is distinct from a key with no values at all
			// ("(present, no value)" above); label it so it does not print as a blank.
			if v == "" {
				fmt.Fprintln(w, "(empty value)"+suffix)
				continue
			}
			writeWrappedSuffix(w, valueCol, v, suffix)
		}
	}
}

// writeWrapped prints value followed by a newline, indenting every line after an
// embedded newline to col so a multi-line value stays aligned under its first
// line instead of falling back to column 0. It is [writeWrappedSuffix] with no
// suffix.
func writeWrapped(w io.Writer, col int, value string) {
	writeWrappedSuffix(w, col, value, "")
}

// writeWrappedSuffix is [writeWrapped] that appends suffix to the first rendered
// line only, so a row can be flagged (e.g. a single-valued conflict's "(conflict)"
// marker) without disturbing the alignment of a multi-line value's continuation
// lines. suffix is a fixed, non-file-derived label, so it is appended after the
// per-line sanitize rather than through it.
func writeWrappedSuffix(w io.Writer, col int, value, suffix string) {
	indent := strings.Repeat(" ", col)
	lines := strings.Split(value, "\n")
	// A trailing newline yields a final empty element; drop it so it does not
	// print as a stray indent-only line. Internal blank lines are preserved.
	if n := len(lines); n > 1 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, line := range lines {
		// Trim a trailing CR (from a CRLF split) first, then escape any control
		// bytes that survive - an embedded mid-line CR/ESC/BEL, the actual
		// injection vector. Legitimate tabs and the line break (owned by the split
		// above) are preserved.
		line = tag.SanitizeText(strings.TrimSuffix(line, "\r"))
		if i == 0 {
			line += suffix
		} else {
			fmt.Fprint(w, indent)
		}
		fmt.Fprintln(w, line)
	}
}

// sanitizeJoin escapes each value for a single-line display (via [tag.SanitizeLine])
// and joins them with sep, so a multi-value field built from untrusted file bytes
// can neither inject control sequences nor - via an embedded newline - forge a line
// in the joined row.
func sanitizeJoin(vals []string, sep string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = tag.SanitizeLine(v)
	}
	return strings.Join(out, sep)
}

// renderPictures prints one line per embedded picture.
func renderPictures(w io.Writer, pics []wl.Picture) {
	if len(pics) == 0 {
		return
	}
	fmt.Fprintf(w, "  pictures (%d):\n", len(pics))
	for _, p := range pics {
		dim := "?"
		if p.Width > 0 && p.Height > 0 {
			dim = fmt.Sprintf("%dx%d", p.Width, p.Height)
		}
		// p.Type is an enum (safe), p.MIME is file-derived text; both are single-line
		// columns, so escape via SanitizeLine (also escapes \n/\t, so neither can break
		// the column layout or forge a line). p.Description is left alone: it prints via
		// %q below, which independently escapes control chars, \n/\t, invalid UTF-8, and
		// DEL/C1 - meeting both tiers - so sanitizing it too would double-escape.
		fmt.Fprintf(w, "    %-12s %-22s %-9s %s\n", tag.SanitizeLine(p.Type.String()), tag.SanitizeLine(p.MIME), dim, wl.HumanBytes(int64(len(p.Data))))
		if p.Description != "" {
			fmt.Fprintf(w, "      %q\n", p.Description)
		}
	}
}

// renderChapters prints one line per navigation chapter with its start timestamp.
func renderChapters(w io.Writer, chs []wl.Chapter) {
	if len(chs) == 0 {
		return
	}
	fmt.Fprintf(w, "  chapters (%d):\n", len(chs))
	for i, c := range chs {
		// A chapter title is file-derived and rendered on a single line, so escape via
		// SanitizeLine (escapes \n/\t too, so a title cannot forge a chapter line).
		title := tag.SanitizeLine(c.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		fmt.Fprintf(w, "    %s  %s\n", wl.FormatChapterTime(c.Start), title)
	}
}

// renderWarnings prints the parse warnings (already "[code] message" formatted).
func renderWarnings(w io.Writer, ws []wl.Warning) {
	if len(ws) == 0 {
		return
	}
	fmt.Fprintf(w, "  warnings (%d):\n", len(ws))
	for _, x := range ws {
		// A warning message can embed a file-derived snippet, but Warning.String now
		// self-sanitizes, so it is safe to print directly (the output boundary is a
		// second backstop).
		fmt.Fprintf(w, "    %s\n", x.String())
	}
}

// renderNative prints the native block summary and the per-source view.
func renderNative(w io.Writer, doc *wl.Document) {
	if nd := doc.Native(); nd != nil {
		if entries := nd.Describe(); len(entries) > 0 {
			fmt.Fprintf(w, "  native blocks (%d):\n", len(entries))
			for _, e := range entries {
				// Both Kind and Note are file-controlled for some formats (a Matroska
				// segment title, attachment name/MIME, or native SimpleTag value), and
				// each is one single-line column, so escape via SanitizeLine.
				note := ""
				if e.Note != "" {
					note = "  - " + tag.SanitizeLine(e.Note)
				}
				fmt.Fprintf(w, "    %-18s %8s%s\n", tag.SanitizeLine(e.Kind), wl.HumanBytes(int64(e.Size)), note)
			}
		}
	}
	if fams := doc.Families(); len(fams) > 0 {
		fmt.Fprintf(w, "  sources (%d):\n", len(fams))
		for _, f := range fams {
			flag := ""
			if !f.Selected {
				flag = "  (conflict)"
			}
			// f.Family is an enum (safe); f.Key and f.Values are file-derived, so escape
			// them. Both the key and the joined values are single-line, so SanitizeLine
			// (via sanitizeJoin) escapes \n/\t as well as the terminal-hijack class.
			fmt.Fprintf(w, "    %-20s %-8s %s%s\n", tag.SanitizeLine(string(f.Key)), f.Family, sanitizeJoin(f.Values, ", "), flag)
		}
	}
}

// renderReport prints a write plan: its operations, size change, padding, and
// warnings. A no-op plan reports that the file is already up to date.
func renderReport(w io.Writer, path string, plan *wl.Plan) {
	r := plan.Report()
	name := displayName(path)
	if plan.IsNoOp() {
		fmt.Fprintf(w, "%s: no changes (already up to date)\n", name)
		return
	}
	fmt.Fprintf(w, "%s: plan\n", name)
	renderChanges(w, plan.Changes())
	if len(r.Operations) == 0 {
		fmt.Fprintln(w, "  - rewrite metadata")
	}
	for _, op := range r.Operations {
		fmt.Fprintf(w, "  - %s\n", op)
	}
	fmt.Fprintf(w, "  size:    %s -> %s\n", wl.HumanBytes(r.BytesBefore), wl.HumanBytes(r.BytesAfter))
	if r.PaddingAfter > 0 {
		// Surface the control alongside the value so the padding is discoverable
		// (the default is the deliberate 8 KiB FLAC-ecosystem convention).
		fmt.Fprintf(w, "  padding: %s  (--padding N / --no-padding to change)\n", wl.HumanBytes(r.PaddingAfter))
	}
	for _, x := range r.Warnings {
		// Plan-time warnings are library-generated today, but Warning.String
		// self-sanitizes (and the output boundary backstops it), so a future plan
		// warning that embeds a file-derived snippet is safe.
		fmt.Fprintf(w, "  warning: %s\n", x.String())
	}
}

// renderChanges prints the field-level change preview (which keys are added,
// removed, or changed) under a "changes:" heading, reusing the diff markers. It
// prints nothing when the plan changes no fields, so a picture-only or
// container-only rewrite shows just its operations.
func renderChanges(w io.Writer, changes []tag.Change) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w, "  changes:")
	for _, c := range changes {
		renderChangeLine(w, "    ", c)
	}
}
