package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// renderDocument writes the human-readable view of a parsed file.
func renderDocument(w io.Writer, path string, doc *wl.Document, native bool) {
	fmt.Fprintf(w, "%s\n", path)
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
	codec := t.Codec
	if codec == "" {
		codec = p.Container
	}
	var parts []string
	if codec != "" {
		parts = append(parts, strings.ToUpper(codec))
	}
	if t.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%d Hz", t.SampleRate))
	}
	if t.Channels > 0 {
		parts = append(parts, fmt.Sprintf("%d ch", t.Channels))
	}
	if t.BitsPerSample > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", t.BitsPerSample))
	}
	if d := p.Duration(); d > 0 {
		parts = append(parts, humanDuration(d))
	}
	if t.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", t.Bitrate/1000))
	}
	return strings.Join(parts, ", ")
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
	fmt.Fprintf(w, "  tags (%d):\n", ts.Len())
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
		if len(vals) == 0 {
			fmt.Fprintf(w, "    %-*s  (present, no value)\n", width, k)
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "    %-*s  ", width, k)
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
		line = strings.TrimSuffix(line, "\r")
		if i > 0 {
			fmt.Fprint(w, indent)
		}
		fmt.Fprintln(w, line)
	}
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
		fmt.Fprintf(w, "    %-12s %-22s %-9s %s\n", p.Type, p.MIME, dim, humanBytes(int64(len(p.Data))))
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
		title := c.Title
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
		fmt.Fprintf(w, "    %s\n", x)
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
					note = "  — " + e.Note
				}
				fmt.Fprintf(w, "    %-18s %8s%s\n", e.Kind, humanBytes(int64(e.Size)), note)
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
			fmt.Fprintf(w, "    %-20s %-8s %s%s\n", f.Key, f.Family, strings.Join(f.Values, ", "), flag)
		}
	}
}

// renderReport prints a write plan: its operations, size change, padding, and
// warnings. A no-op plan reports that the file is already up to date.
func renderReport(w io.Writer, path string, plan *wl.Plan) {
	r := plan.Report()
	if plan.IsNoOp() {
		fmt.Fprintf(w, "%s: no changes (already up to date)\n", path)
		return
	}
	fmt.Fprintf(w, "%s: plan\n", path)
	if len(r.Operations) == 0 {
		fmt.Fprintln(w, "  - rewrite metadata")
	}
	for _, op := range r.Operations {
		fmt.Fprintf(w, "  - %s\n", op)
	}
	fmt.Fprintf(w, "  size:    %s -> %s\n", humanBytes(r.BytesBefore), humanBytes(r.BytesAfter))
	if r.PaddingAfter > 0 {
		fmt.Fprintf(w, "  padding: %s\n", humanBytes(r.PaddingAfter))
	}
	for _, x := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", x)
	}
}
