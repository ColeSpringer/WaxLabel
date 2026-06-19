package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// displayName maps a path to its display form for a text record header: the "-"
// stdin sentinel reads as "<stdin>", and every real path is shown verbatim. It is
// purely cosmetic - readInputs already keeps the buffered temp path out of the
// renderers (they receive the original "-" arg, never the temp path). JSON output
// keeps the raw "-" so a script still keys on the argument it passed.
func displayName(path string) string {
	if path == stdinArg {
		return "<stdin>"
	}
	return path
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
		// t.Codec is already the canonical name (CanonicalCodec, applied at parse); the
		// text view just uppercases it to read consistently with the rest of the line.
		parts = append(parts, strings.ToUpper(t.Codec))
	case p.Container != "":
		// No codec was identified (e.g. an unrecognized Matroska/MP4 track): name the
		// container and say so, rather than printing the container as if it were the
		// codec (a bare "MATROSKA"). This "codec unknown" form is itself an
		// informative signal (the container parsed; the codec did not resolve), so
		// keep the line even when no technical properties accompany it - unlike a bare
		// codec name, which would just be noise.
		parts = append(parts, fmt.Sprintf("%s (codec unknown)", p.Container))
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
	// would print as a misleading "0 kbps", so it is omitted instead.
	if t.Bitrate >= 1000 {
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
		// key from a malformed file cannot inject control bytes into the line either.
		ks := tag.SanitizeText(string(k))
		if len(vals) == 0 {
			fmt.Fprintf(w, "    %-*s  (present, no value)\n", width, ks)
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "    %-*s  ", width, ks)
			// A present-but-empty value is distinct from a key with no values at all
			// ("(present, no value)" above); label it so it does not print as a blank.
			if v == "" {
				fmt.Fprintln(w, "(empty value)")
				continue
			}
			writeWrapped(w, valueCol, v)
		}
	}
}

// writeWrapped prints value followed by a newline, indenting every line after an
// embedded newline to col so a multi-line value stays aligned under its first
// line instead of falling back to column 0.
func writeWrapped(w io.Writer, col int, value string) {
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
		if i > 0 {
			fmt.Fprint(w, indent)
		}
		fmt.Fprintln(w, line)
	}
}

// sanitizeJoin escapes each value for human display (via [tag.SanitizeText]) and
// joins them with sep, so a multi-value field built from untrusted file bytes
// cannot inject control sequences into the joined line.
func sanitizeJoin(vals []string, sep string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = tag.SanitizeText(v)
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
		// p.Type is an enum (safe), p.MIME is file-derived text; sanitize both raw
		// %s fields. p.Description is left alone: it prints via %q below, which
		// already escapes control chars - sanitizing it too would double-escape.
		fmt.Fprintf(w, "    %-12s %-22s %-9s %s\n", tag.SanitizeText(p.Type.String()), tag.SanitizeText(p.MIME), dim, wl.HumanBytes(int64(len(p.Data))))
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
		title := tag.SanitizeText(c.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		fmt.Fprintf(w, "    %s  %s\n", chapterTimestamp(c.Start), title)
	}
}

// chapterTimestamp formats a chapter start as H:MM:SS.mmm (millisecond precision,
// since adjacent chapters can be seconds apart).
func chapterTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
}

// renderWarnings prints the parse warnings (already "[code] message" formatted).
func renderWarnings(w io.Writer, ws []wl.Warning) {
	if len(ws) == 0 {
		return
	}
	fmt.Fprintf(w, "  warnings (%d):\n", len(ws))
	for _, x := range ws {
		// A warning message can embed a file-derived snippet, so escape it for display.
		fmt.Fprintf(w, "    %s\n", tag.SanitizeText(x.String()))
	}
}

// renderNative prints the native block summary and the per-source view.
func renderNative(w io.Writer, doc *wl.Document) {
	if nd := doc.Native(); nd != nil {
		if entries := nd.Describe(); len(entries) > 0 {
			fmt.Fprintf(w, "  native blocks (%d):\n", len(entries))
			for _, e := range entries {
				note := ""
				if e.Note != "" {
					note = "  - " + e.Note
				}
				fmt.Fprintf(w, "    %-18s %8s%s\n", e.Kind, wl.HumanBytes(int64(e.Size)), note)
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
			// f.Family is an enum (safe); f.Key and f.Values are file-derived, so escape them.
			fmt.Fprintf(w, "    %-20s %-8s %s%s\n", tag.SanitizeText(string(f.Key)), f.Family, sanitizeJoin(f.Values, ", "), flag)
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
		// Plan-time warnings are library-generated today, but sanitize for parity with
		// the dump path (renderWarnings) and to stay safe if a future plan warning
		// ever embeds a file-derived snippet (e.g. a chapter title).
		fmt.Fprintf(w, "  warning: %s\n", tag.SanitizeText(x.String()))
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
