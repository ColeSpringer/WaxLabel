package main

import (
	"cmp"
	"fmt"
	"io"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// displayName is the text header path: "-" becomes "<stdin>"; real paths pass through
// [tag.SanitizeLine] so hostile names cannot forge lines in a multi-file listing.
// JSON keeps the raw path so scripts can key on the argument they passed.
func displayName(path string) string {
	if s := stdinDisplay(path); s != "" {
		return s
	}
	return tag.SanitizeLine(path)
}

// stdinDisplay returns "<stdin>" for the "-" sentinel, or "" for a real path.
// Shared by displayName and jsonFileName so both outputs label stdin the same way.
func stdinDisplay(path string) string {
	if path == stdinArg {
		return "<stdin>"
	}
	return ""
}

// jsonFileName maps "-" to "<stdin>" for JSON "file"; real paths are unchanged (no SanitizeLine).
func jsonFileName(path string) string {
	if s := stdinDisplay(path); s != "" {
		return s
	}
	return path
}

// renderDocument writes the human-readable view of a parsed file.
func renderDocument(w io.Writer, path string, doc *wl.Document, native bool) {
	fmt.Fprintf(w, "%s\n", displayName(path))
	// Container name when it differs from the codec family (RF64/BW64, AIFC, WebM).
	fmt.Fprintf(w, "  format:  %s\n", transferFormatLabel(doc.Format(), doc.Properties().Container))
	if line := audioLine(doc.Properties()); line != "" {
		fmt.Fprintf(w, "  audio:   %s\n", line)
	}
	// Omitted when the format has no padding region (avoid reporting 0).
	if pad := doc.Padding(); pad > 0 {
		fmt.Fprintf(w, "  padding: %s\n", wl.HumanBytes(pad))
	}
	renderTags(w, doc.Tags())
	// Legacy-only tags and opaque legacy content are separate notes; see --native for detail.
	if lo := doc.LegacyOnlyKeys(); len(lo) > 0 {
		fmt.Fprintf(w, "  note:    %s only in a legacy container; see --native\n", pluralUnit(len(lo), "tag"))
	}
	if doc.HasOpaqueLegacyContent() {
		fmt.Fprintln(w, "  note:    a legacy container holds non-tag metadata not shown; see --native")
	}
	renderPictures(w, doc.Pictures())
	renderChapters(w, doc.Chapters())
	renderSyncedLyrics(w, doc.SyncedLyrics())
	warnings := doc.Warnings()
	renderWarnings(w, warnings)
	// Parse warnings are a subset; lint finds malformed dates, cardinality, custom keys, etc.
	if len(warnings) > 0 {
		fmt.Fprintln(w, `  run "waxlabel lint" for the full issue set (e.g. malformed dates, custom keys)`)
	}
	if native {
		renderNative(w, doc)
	}
}

// audioLine summarizes the first audio track on one comma-separated line.
func audioLine(p wl.Properties) string {
	t := p.First()
	var parts []string
	// Drop a bare codec name with no rate/channels/duration (e.g. empty.mp3 header noise).
	hasSubstantive := false
	switch {
	case t.Codec != "":
		// Canonical codec name, rendered verbatim to match --json; escape for one-line output.
		parts = append(parts, tag.SanitizeLine(t.Codec))
	case p.Container != "":
		// Container parsed but codec did not; keep the line even without rate/channels.
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
	// Bit depth only for codecs with fixed sample width; lossy containers often lie (e.g. AAC in MP4).
	if t.BitsPerSample > 0 && bitDepthMeaningful(t.Codec) {
		parts = append(parts, fmt.Sprintf("%d-bit", t.BitsPerSample))
		hasSubstantive = true
	}
	if d := p.Duration(); d > 0 {
		parts = append(parts, humanDuration(d))
		hasSubstantive = true
	}
	// Omit sub-1 kbps (would round to 0) and zero-duration files (header bitrate is meaningless).
	if t.Bitrate >= 1000 && p.Duration() > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", t.Bitrate/1000))
		hasSubstantive = true
	}
	if t.OutputGain != 0 {
		parts = append(parts, "gain "+wl.OutputGainDB(t.OutputGain))
		hasSubstantive = true
	}
	if !hasSubstantive {
		return ""
	}
	return strings.Join(parts, ", ")
}

// bitDepthMeaningful is true when bits-per-sample describes stored samples (not lossy decode depth).
// Blacklist the small lossy set; DTS omitted because DTS-HD Master is lossless under the same name.
func bitDepthMeaningful(codec string) bool {
	switch strings.ToUpper(codec) {
	case "AAC", "MP1", "MP2", "MP3", "OPUS", "VORBIS", "AC-3", "E-AC-3", "MPC", "MUSEPACK",
		"WMA V1", "WMA V2", "WMA PRO", "WMA VOICE":
		return false
	}
	return true
}

// keyColumn caps tag key column width.
const keyColumn = 24

// dupOrConflict labels duplicate vs conflicting values on a single-valued key (matches lint).
// Marker includes leading two spaces for direct append.
func dupOrConflict(k tag.Key, vals []string) (marker string, conflict bool) {
	switch cardinalityState(k, vals) {
	case "duplicate":
		return "  (duplicate)", false
	case "conflict":
		return "  (conflict)", true
	}
	return "", false
}

// cardinalityState classifies duplicate vs conflict on a single-valued key (shared by text and JSON).
func cardinalityState(k tag.Key, vals []string) string {
	if !k.SingleValuedMulti(len(vals)) {
		return ""
	}
	if tag.DistinctValues(vals) == 1 {
		return "duplicate"
	}
	return "conflict"
}

// renderTags prints tags: one value per line, key repeated for multi-valued fields.
func renderTags(w io.Writer, ts tag.TagSet) {
	if ts.Len() == 0 {
		fmt.Fprintln(w, "  tags:    (none)")
		return
	}
	// Header counts distinct keys; conflict count includes extra "(conflict)" rows below.
	n := ts.Len()
	width, conflicts := 0, 0
	for _, k := range ts.Keys() {
		if len(k) > width {
			width = len(k)
		}
		if !k.SingleValuedMulti(ts.ValueCount(k)) {
			continue
		}
		vals, _ := ts.Get(k)
		if _, conflict := dupOrConflict(k, vals); conflict {
			conflicts++
		}
	}
	if width > keyColumn {
		width = keyColumn
	}
	fmt.Fprintf(w, "  tags (%s", pluralUnit(n, "key"))
	if conflicts > 0 {
		fmt.Fprintf(w, ", %d in conflict", conflicts)
	}
	fmt.Fprintln(w, "):")
	valueCol := 4 + width + 2
	for k, vals := range ts.All() {
		// Defensive sanitize: hostile keys could forge tag lines (SanitizeLine for single-line keys).
		ks := tag.SanitizeLine(string(k))
		if len(vals) == 0 {
			fmt.Fprintf(w, "    %-*s  (present, no value)\n", width, ks)
			continue
		}
		suffix, _ := dupOrConflict(k, vals)
		for _, v := range vals {
			fmt.Fprintf(w, "    %-*s  ", width, ks)
			if v == "" {
				fmt.Fprintln(w, "(empty value)"+suffix)
				continue
			}
			// Elide huge values for terminal output; JSON keeps full bytes (shared tag.ElideValue).
			if proseKeys[k] {
				writeWrappedSuffix(w, valueCol, tag.ElideValue(v), suffix)
				continue
			}
			fmt.Fprintln(w, tag.SanitizeLine(tag.ElideValue(v))+suffix)
		}
	}
}

// proseKeys are multi-line text fields; other values print on one escaped line.
var proseKeys = map[tag.Key]bool{tag.Lyrics: true, tag.Comment: true, tag.Description: true, tag.LongDescription: true}

// writeWrapped prints a multi-line value with continuation lines indented to col.
func writeWrapped(w io.Writer, col int, value string) {
	writeWrappedSuffix(w, col, value, "")
}

// writeWrappedSuffix is writeWrapped with suffix on the first line only (fixed label, not sanitized).
func writeWrappedSuffix(w io.Writer, col int, value, suffix string) {
	indent := strings.Repeat(" ", col)
	lines := strings.Split(value, "\n")
	// Drop trailing empty element from a final newline; keep internal blank lines.
	if n := len(lines); n > 1 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, line := range lines {
		line = tag.SanitizeText(strings.TrimSuffix(line, "\r"))
		if i == 0 {
			line += suffix
		} else {
			fmt.Fprint(w, indent)
		}
		fmt.Fprintln(w, line)
	}
}

// sanitizeJoin escapes values and joins with sep for safe single-line display.
func sanitizeJoin(vals []string, sep string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = tag.SanitizeLine(v)
	}
	return strings.Join(out, sep)
}

// pictureRow formats type, MIME, dimensions, and size on one line (shared by dump and plan C4a).
func pictureRow(p wl.Picture) string {
	dim := "--"
	if p.Width > 0 && p.Height > 0 {
		dim = fmt.Sprintf("%dx%d", p.Width, p.Height)
	}
	// Depth/colors append to size column; colors only for indexed images.
	size := wl.HumanBytes(int64(len(p.Data)))
	if p.Depth > 0 {
		if p.Colors > 0 {
			size = fmt.Sprintf("%s (%d-bit, %d colors)", size, p.Depth, p.Colors)
		} else {
			size = fmt.Sprintf("%s (%d-bit)", size, p.Depth)
		}
	}
	return fmt.Sprintf("%-12s %-22s %-9s %s", tag.SanitizeLine(p.Type.String()), tag.SanitizeLine(p.MIME), dim, size)
}

// renderPictures lists pictures 1-based (matches --remove-picture index and doc.Pictures order).
func renderPictures(w io.Writer, pics []wl.Picture) {
	if len(pics) == 0 {
		return
	}
	fmt.Fprintf(w, "  pictures (%d):\n", len(pics))
	for i, p := range pics {
		fmt.Fprintf(w, "    %d. %s\n", i+1, pictureRow(p))
		if p.Description != "" {
			// %q escapes description; do not SanitizeLine (would double-escape).
			fmt.Fprintf(w, "       %q\n", p.Description)
		}
	}
}

// renderChapters prints one line per navigation chapter with its start timestamp.
func renderChapters(w io.Writer, chs []wl.Chapter) {
	if len(chs) == 0 {
		return
	}
	fmt.Fprintf(w, "  chapters (%d):\n", len(chs))
	for _, c := range chs {
		title := tag.SanitizeLine(c.Title)
		// JSON omits empty titles; use "(untitled)" rather than inventing "Chapter N".
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(w, "    %s  %s%s\n", wl.FormatChapterTime(c.Start), title, chapterAnnotations(c))
	}
}

// chapterAnnotations adds [lang: …], (hidden), and (disabled) suffixes; "" for the common case.
func chapterAnnotations(c wl.Chapter) string {
	var b strings.Builder
	if lang := cmp.Or(c.Language, c.LanguageIETF); lang != "" {
		fmt.Fprintf(&b, " [lang: %s]", tag.SanitizeLine(lang))
	}
	if c.Hidden {
		b.WriteString(" (hidden)")
	}
	if c.Disabled {
		b.WriteString(" (disabled)")
	}
	return b.String()
}

// renderSyncedLyrics prints synced lyric sets; timestamps match chapter format.
func renderSyncedLyrics(w io.Writer, sets []wl.SyncedLyrics) {
	if len(sets) == 0 {
		return
	}
	// Header names both set count and line count (bare N would read as lines).
	lines := 0
	for _, sl := range sets {
		lines += len(sl.Lines)
	}
	fmt.Fprintf(w, "  synced lyrics (%s, %s):\n", pluralUnit(len(sets), "set"), pluralUnit(lines, "line"))
	for i, sl := range sets {
		if h := syncedLyricsHeader(sl, i, len(sets)); h != "" {
			fmt.Fprintf(w, "    %s\n", h)
		}
		for _, ln := range sl.Lines {
			fmt.Fprintf(w, "      %s  %s\n", wl.FormatChapterTime(ln.Time), syncedLineText(ln.Text))
		}
	}
}

// pluralUnit formats "1 key" vs "5 keys" for dump headers that name their unit.
func pluralUnit(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// syncedLyricsHeader renders [lang/desc] and optional "set N"; "" for a single unlabeled set.
func syncedLyricsHeader(sl wl.SyncedLyrics, idx, total int) string {
	var notes []string
	if sl.Language != "" {
		notes = append(notes, "lang: "+tag.SanitizeLine(sl.Language))
	}
	if sl.Description != "" {
		notes = append(notes, "desc: "+tag.SanitizeLine(sl.Description))
	}
	if len(notes) == 0 && total <= 1 {
		return ""
	}
	label := ""
	if total > 1 {
		label = fmt.Sprintf("set %d", idx+1)
	}
	if len(notes) == 0 {
		return label
	}
	joined := "[" + strings.Join(notes, ", ") + "]"
	if label != "" {
		return label + " " + joined
	}
	return joined
}

// syncedLineText renders lyric text; empty becomes "(blank)".
func syncedLineText(s string) string {
	if t := tag.SanitizeLine(s); t != "" {
		return t
	}
	return "(blank)"
}

// renderWarnings prints the parse warnings (already "[code] message" formatted).
func renderWarnings(w io.Writer, ws []wl.Warning) {
	if len(ws) == 0 {
		return
	}
	fmt.Fprintf(w, "  warnings (%d):\n", len(ws))
	for _, x := range ws {
		fmt.Fprintf(w, "    %s\n", x.String())
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
					note = "  - " + tag.SanitizeLine(e.Note)
				}
				fmt.Fprintf(w, "    %-18s %8s%s\n", tag.SanitizeLine(e.Kind), nativeSize(e), note)
			}
		}
	}
	if fams := doc.Families(); len(fams) > 0 {
		fmt.Fprintf(w, "  families (%d):\n", len(fams))
		fmt.Fprintln(w, "    (provenance of tag values across containers)")
		for _, f := range fams {
			flag := ""
			if !f.Selected {
				flag = "  (conflict)"
			}
			fmt.Fprintf(w, "    %-20s %-8s %s%s\n", tag.SanitizeLine(string(f.Key)), f.Family, sanitizeJoin(f.Values, ", "), flag)
		}
	}
}

// nativeSize formats size as "N unit", bytes, blank, or "0 B" for empty PADDING.
func nativeSize(e wl.NativeEntry) string {
	switch {
	case e.Unit != "":
		return fmt.Sprintf("%d %s", e.Size, tag.SanitizeLine(e.Unit))
	case e.Size > 0:
		return wl.HumanBytes(int64(e.Size))
	case e.Kind == "PADDING":
		return "0 B"
	default:
		return ""
	}
}

// renderReport prints a write plan. addedPics detail picture adds under the count change (C4a).
func renderReport(w io.Writer, path string, plan *wl.Plan, addedPics []wl.Picture) {
	r := plan.Report()
	name := displayName(path)
	if plan.IsNoOp() {
		// No-op may still warn (e.g. value dropped); headline matches Plan.String via HasDiscardWarning.
		fmt.Fprintf(w, "%s: %s\n", name, wl.NoChangesLine(wl.HasDiscardWarning(r.Warnings)))
		for _, x := range r.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", x.String())
		}
		return
	}
	fmt.Fprintf(w, "%s: plan\n", name)
	renderChanges(w, plan.Changes(), addedPics)
	// Separate "operations:" heading so "-" removals above are not confused with operation lines.
	fmt.Fprintln(w, "  operations:")
	if len(r.Operations) == 0 {
		fmt.Fprintln(w, "    rewrite metadata")
	}
	for _, op := range r.Operations {
		fmt.Fprintf(w, "    %s\n", op)
	}
	fmt.Fprintf(w, "  size:    %s -> %s\n", wl.HumanBytes(r.BytesBefore), wl.HumanBytes(r.BytesAfter))
	if r.PaddingAfter > 0 {
		fmt.Fprintf(w, "  padding: %s  (--padding N / --no-padding to change)\n", wl.HumanBytes(r.PaddingAfter))
	} else if wl.CapabilitiesFor(r.Format).Padding != wl.AccessNone {
		// Only for formats with user-controllable padding (WAV/Ogg/etc. would contradict their note).
		fmt.Fprintln(w, "  padding: none")
	}
	for _, x := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", x.String())
	}
}

// Lowercase pseudo-keys for set-count changes (see countChange in plan.go); avoid canonical key collision.
const (
	picturesCountKey     = "pictures"
	chaptersCountKey     = "chapters"
	syncedLyricsCountKey = "synced lyrics"
)

// isCountChange is true for synthetic set-count keys (JSON emits integer Count, not Old/New strings).
func isCountChange(k tag.Key) bool {
	return k == picturesCountKey || k == chaptersCountKey || k == syncedLyricsCountKey
}

// renderChanges prints field diffs; lists addedPics under picture count changes (C4a).
func renderChanges(w io.Writer, changes []tag.Change, addedPics []wl.Picture) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w, "  changes:")
	for _, c := range changes {
		renderChangeLine(w, "    ", c)
		if c.Key == picturesCountKey && (c.Kind == tag.ChangeAdded || c.Kind == tag.ChangeChanged) {
			for _, p := range addedPics {
				fmt.Fprintf(w, "      + %s\n", pictureRow(p))
				if p.Description != "" {
					fmt.Fprintf(w, "          %q\n", p.Description)
				}
			}
		}
	}
}
