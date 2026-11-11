package aiff

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan builds a preservation-first rewrite: keep chunk order, copy SSND verbatim,
// re-render tag containers, recompute FORM size. Tag precedence: see package doc.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("aiff: edited media has no AIFF native document")
	}

	textPresent := len(d.textIdx) > 0
	id3Present := d.id3 != nil

	tagsChanged := !base.Tags.Equal(edited.Tags) || !core.EqualTextItems(base, edited)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	// LegacyStrip consolidates tags into the ID3 chunk: it drops the native chunks and never
	// creates one.
	legacyStrip := opts.Legacy == core.LegacyStrip
	stripText := legacyStrip && textPresent
	// Keys whose text chunks this write re-renders: those whose value moved plus those
	// the edit named outright. Setting the already-projected value is how a caller
	// resolves a chunk the ID3 chunk disagrees with.
	changed := core.ChangedKeys(base.Tags, edited.Tags, opts.Touched)

	report := core.WriteReport{Format: core.FormatAIFF, BytesBefore: edited.Identity.Size}

	// One WriteOpts for both the predicate and the rebuild, so they cannot disagree.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// A requested write encoding (--numeric-genre) changes how a value is stored, not the
	// value itself, so the tag comparison above cannot see it.
	encodingRewrite := id3.EncodingRewriteNeeded(d.id3, edited.Tags, wopts)

	// Decide which containers receive the edited tags. Chapters, synced lyrics and a comment's
	// or lyrics' description and language (ID3 CHAP/CTOC and SYLT frames, COMM/USLT fields)
	// force an ID3 chunk, since native text cannot store them.
	needID3 := id3Present || len(edited.Pictures) > 0 || len(edited.Chapters) > 0 || len(edited.SyncedLyrics) > 0 ||
		id3.NeedsTextFrames(edited) || !textRepresentable(edited.Tags, changed) || legacyStrip
	writeText := (textPresent && !stripText) || !needID3

	// Each native text chunk's fate: a changed key's re-rendered, every other kept whole. The
	// zero plan drops them all, as a strip does.
	tp := textPlan{at: make([]textDecision, len(d.texts))}
	if writeText {
		tp = rebuildText(d.texts, edited.Tags, changed)
	}
	// An explicit set of a key whose text chunk disagreed with the projection changes the
	// chunks while leaving the canonical set as it was; it must defeat both no-op gates.
	textRewrite := writeText && textPresent && tp.rewrites()

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy flagged NoOp, so
	// SaveAsFile/WriteTo still produce a whole file and SaveBack skips it. A chapters-
	// or synced-lyrics-only edit must defeat the gate too.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !stripText && !encodingRewrite && !textRewrite {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// Re-check the ID3 CTOC count at the codec boundary. Only a chapter edit re-renders
	// the CTOC, so unchanged chapters can keep their source frames.
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	// id3.RewriteBase picks the diff base: empty with no ID3 chunk, the parsed ID3
	// frames alone under --legacy strip (so native-only text is emitted into ID3), and
	// the merged base otherwise.
	var newID3 *id3.Tag
	var id3Info id3.RebuildInfo
	if needID3 {
		srcTag := d.id3
		if srcTag == nil {
			// A new chunk uses the per-format default (v2.4 for AIFF and AIFF-C alike, see
			// core.DefaultID3Version); an existing chunk keeps its own version.
			srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatAIFF))
		}
		version := srcTag.WriteVersion()
		id3Base := id3.RewriteBase(base.Tags, srcTag, id3Present, stripText)
		var frames []id3.Frame
		frames, id3Info = id3.RebuildFrames(srcTag.Frames(), id3Base, edited.Tags, version,
			id3.StructuredEdit{
				Pictures: edited.Pictures, PicturesChanged: picturesChanged,
				Chapters: edited.Chapters, ChaptersChanged: chaptersChanged,
				SyncedLyrics: edited.SyncedLyrics, SyncedLyricsChanged: syncedLyricsChanged,
				Comments: edited.Comments, CommentsChanged: !slices.Equal(base.Comments, edited.Comments),
				Lyrics: edited.Lyrics, LyricsChanged: !slices.Equal(base.Lyrics, edited.Lyrics),
				Carried:             opts.Carried,
				SyncedLyricsCleared: opts.SyncedLyricsCleared,
				MediaDuration:       edited.Properties.Duration(),
			}, wopts)
		if err := id3.CheckSize(version, frames, bits.DefaultLimits.MaxElements); err != nil {
			return nil, err
		}
		if err := id3.RebuildError(id3Info); err != nil {
			return nil, err
		}
		newID3 = srcTag.WithFrames(frames)
	}

	// An empty container is not emitted (no point writing a header-only chunk);
	// this also lets a full clear drop the container.
	emitText := tp.holds()
	emitID3 := needID3 && newID3 != nil && len(newID3.Frames()) > 0
	// A chunk whose frames the edit leaves alone is copied whole, padding included.
	id3Kept := id3.ChunkKept(d.id3, newID3)
	if id3Kept {
		newID3 = d.id3
	}

	// When both native text chunks and ID3 are emitted, any multi-valued key backed by a
	// single-valued text chunk keeps its full set only in ID3.
	if emitText && emitID3 {
		report.Warnings = append(report.Warnings, nativeReducedWarnings(edited.Tags, changed)...)
	}

	outs, ops, dropped, staleDropped := planChunks(d, tp, newID3, emitText, emitID3, id3Kept, stripText)

	segs, lay, err := assemble(d, outs)
	if err != nil {
		return nil, err
	}
	report.Operations = ops
	// A conflict is resolved only in chunks the output holds, whichever side the edit moved;
	// a strip or a full clear drops the chunks outright.
	if emitText {
		for _, k := range textConflictKeys(base.Families, changed) {
			report.Operations = append(report.Operations, "native text chunk conflict resolved ("+string(k)+")")
		}
	}
	if emitID3 {
		// The embedded-container op lines (pictures/chapters/synced lyrics) come from the shared
		// id3.ContainerOps, which owns the change-flag-and-count gate.
		report.Operations = append(report.Operations, id3.ContainerOps(
			picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters),
			syncedLyricsChanged, len(edited.SyncedLyrics))...)
	}
	if stripText {
		// LegacyStrip moves the native values into the ID3 chunk, but a value the projection
		// did not select has no canonical key to carry it: dropping the chunks destroys it.
		// doc.go requires a warning for such a loss.
		if keys := core.StrippedFamilyKeys(base.Families, core.FamilyAIFF, edited.Tags); len(keys) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnLegacyStripDropped,
				core.StripDroppedMessage("native text chunks", []string{core.StrippedValuesLost(keys)}), keys...)
		}
	}
	if n := tp.lostTail(d.texts); n > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnMalformedTagEntryDropped, fmt.Sprintf(
			"%d byte(s) after the NUL that ends a native text chunk's value are not carried into the rewritten file", n))
	}
	if id3Info.UsedV23Multi {
		report.Operations = append(report.Operations, "v2.3 multi-value NUL-separated storage")
		report.Warnings = core.Warn(report.Warnings, core.WarnID3MultiValue,
			"a multi-value field was written NUL-separated in ID3v2.3, a de-facto extension some readers do not split")
	}
	if encodingRewrite {
		report.Operations = append(report.Operations, core.EncodingRewriteOp("genre"))
	}
	report.BytesAfter = lay.total
	report.PaddingAfter = id3.ChunkPaddingAfter(newID3, d.id3, d.id3ChunkLen(), id3Kept, emitID3)

	result := buildResult(edited, d, newID3, lay)
	// Surface ID3 rebuild losses the bytes cannot show. AIFF's native text chunks store
	// no dates, so a v2.3 ID3 date drop or reduction is a file-level loss.
	var written []id3.Frame
	if emitID3 {
		written = newID3.Frames()
	}
	g := id3.NewDupGrader(base, result, d.id3.Frames(), written)
	losses := make([]core.DuplicateContent, len(dropped))
	for i, ch := range dropped {
		losses[i] = g.Loss(ch.dupID3, "unreadable ID3 chunk(s)")
	}
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "ID3 chunk", base.Tags, result.Tags, losses)
	if staleDropped {
		report.Warnings = core.Warn(report.Warnings, core.WarnMalformedTagEntryDropped,
			"the ID3 chunk that could not be read is replaced by the rewritten one and its contents are not carried")
	}
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, id3Info, result.Tags)
	if !id3Kept {
		report.Warnings = id3.AppendChunkDropped(report.Warnings, d.id3, emitID3)
	}
	// Collapse to a no-op when the containers re-projected to base's values.
	// DowngradeNoOp carries the value-dropped warning forward.
	if np := core.DowngradeNoOp(core.FormatAIFF, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), stripText || encodingRewrite || textRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// planChunks builds the output chunk list in source order: each tag chunk kept whole, re-rendered
// where it stands or dropped, and everything else (including the SSND sound chunk) copied
// verbatim. It returns the duplicate ID3 chunks the rewrite drops and whether it drops a stale one,
// one that could not be read, that a fresh chunk replaces.
func planChunks(d *doc, tp textPlan, newID3 *id3.Tag, emitText, emitID3, id3Kept, stripText bool) (outs []outChunk, ops []string, dropped []chunk, staleDropped bool) {
	// A created text chunk goes behind anchor, the last one ahead of the sound, where a reader
	// that stops at SSND still finds it; with none there, just ahead of SSND.
	textAt, anchor := make(map[int]int, len(d.textIdx)), -1
	for j, i := range d.textIdx {
		textAt[i] = j
		if d.ssndIdx < 0 || i < d.ssndIdx {
			anchor = j
		}
	}
	// What the write does to the chunks the file holds, reported where the first one stands.
	textOp := ""
	switch {
	case stripText:
		textOp = "native text chunk strip"
	case !emitText:
		textOp = "native text chunk drop" // the edit emptied every chunk
	case tp.rewrites():
		textOp = "native text chunk rewrite"
	}
	id3Placed := false

	for i, ch := range d.chunks {
		j, isText := textAt[i]
		switch {
		case isText:
			if j == 0 && textOp != "" {
				ops = append(ops, textOp)
			}
			if tp.at[j].keep {
				outs = append(outs, outChunk{id: ch.id, srcOff: ch.bodyOff, bodyLen: ch.bodyLen, text: &d.texts[j]})
			}
			for _, it := range tp.at[j].out {
				outs = append(outs, textOut(it))
			}
			if j == anchor {
				for _, it := range tp.created {
					outs = append(outs, textOut(it))
				}
			}
		case i == d.id3Idx:
			switch {
			case id3Kept:
				outs = append(outs, outChunk{id: ch.id, role: roleID3, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
			case emitID3:
				outs = append(outs, id3Out(newID3, ch.id, d.id3, ch.bodyLen))
				ops = append(ops, "ID3 chunk rewrite")
			default:
				ops = append(ops, "ID3 chunk drop")
			}
			id3Placed = emitID3
		case ch.dupTag:
			// A duplicate ID3 chunk: dropped so the output carries one copy.
			ops = append(ops, "duplicate ID3 chunk drop")
			dropped = append(dropped, ch)
		default:
			// An ID3-identified chunk reaches here only when it is neither the authoritative
			// ID3 nor a marked duplicate: a lone chunk that failed to parse. Drop it when a
			// fresh ID3 chunk is written, so the output never carries two.
			if emitID3 && isID3Chunk(ch.id4()) {
				ops = append(ops, "stale ID3 chunk drop")
				staleDropped = true
				continue
			}
			role := roleOther
			if i == d.ssndIdx {
				role = roleSSND
			}
			outs = append(outs, outChunk{id: ch.id, role: role, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
		}
	}

	// Insert newly created containers (native text then ID3) just before the SSND
	// chunk, the conventional position.
	var created []outChunk
	if anchor < 0 {
		for _, it := range tp.created {
			created = append(created, textOut(it))
		}
		if len(d.textIdx) == 0 && len(created) > 0 {
			ops = append(ops, "native text chunk creation")
		}
	}
	if emitID3 && !id3Placed {
		created = append(created, id3Out(newID3, [4]byte{'I', 'D', '3', ' '}, nil, 0))
		ops = append(ops, "ID3 chunk creation")
	}
	if len(created) > 0 {
		outs = insertBeforeSSND(outs, created)
	}
	return outs, ops, dropped, staleDropped
}

// id3Out builds the ID3 output chunk under id from t, rendered in place of src, the tag the chunk
// held in srcLen bytes (nil for a new chunk).
func id3Out(t *id3.Tag, id [4]byte, src *id3.Tag, srcLen int64) outChunk {
	body := id3.RenderChunk(t, src, srcLen)
	return outChunk{id: id, role: roleID3, body: body, bodyLen: int64(len(body))}
}

// insertBeforeSSND inserts created chunks just before the SSND chunk, or at the
// end when there is no SSND chunk.
func insertBeforeSSND(outs, created []outChunk) []outChunk {
	for i, oc := range outs {
		if oc.role == roleSSND {
			out := make([]outChunk, 0, len(outs)+len(created))
			out = append(out, outs[:i]...)
			out = append(out, created...)
			out = append(out, outs[i:]...)
			return out
		}
	}
	return append(outs, created...)
}

// chunkRole tags an output chunk so the plan's result can re-find the
// ID3 and sound chunks without guessing from identifiers.
type chunkRole uint8

const (
	roleOther chunkRole = iota
	roleID3
	roleSSND
)

// outChunk is one chunk in the planned output: a literal body (re-rendered or
// created) or a verbatim copy from the source.
type outChunk struct {
	id      [4]byte
	role    chunkRole
	body    []byte // literal body; nil means copy bodyLen bytes from srcOff
	srcOff  int64
	bodyLen int64
	text    *textItem // the value a native text chunk holds, nil for any other chunk
}

// outLayout is the byte-level result of assembling the output chunks: the new
// chunk list (with output offsets), the ID3 and sound indices for the plan's
// result, the sound extent, and the total output size.
type outLayout struct {
	chunks   []chunk
	texts    []textItem // the native text chunks, in output order
	ssndIdx  int
	id3Idx   int
	audioOff int64
	audioEnd int64
	total    int64
}

// assemble turns the output chunks into a rewrite segment list and recomputes the
// FORM size, returning the layout needed to build the plan's result. All
// sizes are big-endian, per IFF.
func assemble(d *doc, outs []outChunk) (segs []bits.Segment, lay outLayout, err error) {
	lay = outLayout{ssndIdx: -1, id3Idx: -1}
	var chunksTotal int64
	for _, oc := range outs {
		if oc.bodyLen > math.MaxUint32 {
			return nil, lay, fmt.Errorf("%w: chunk %q body is %d bytes (max %d)",
				waxerr.ErrSizeTooLarge, string(oc.id[:]), oc.bodyLen, int64(math.MaxUint32))
		}
		chunksTotal += 8 + oc.bodyLen + (oc.bodyLen & 1)
	}
	chunksTotal += d.trailingLen
	formSize := 4 + chunksTotal // form type ("AIFF"/"AIFC") + all chunks (in-FORM trailing included)
	// Out-of-FORM trailing is appended after the FORM chunk, not counted in its
	// size, so a strict reader walking by the FORM size does not misparse it.
	lay.total = 8 + formSize + d.outerLen
	if formSize > math.MaxUint32 {
		return nil, lay, fmt.Errorf("%w: AIFF output is %d bytes, exceeding the 4 GiB FORM limit",
			waxerr.ErrSizeTooLarge, lay.total)
	}

	var head [12]byte
	copy(head[0:4], "FORM")
	binary.BigEndian.PutUint32(head[4:8], uint32(formSize))
	copy(head[8:12], d.formType[:])
	segs = append(segs, bits.Lit(head[:]))

	running := int64(12)
	lay.chunks = make([]chunk, 0, len(outs))
	for _, oc := range outs {
		var ch [8]byte
		copy(ch[0:4], oc.id[:])
		binary.BigEndian.PutUint32(ch[4:8], uint32(oc.bodyLen))
		segs = append(segs, bits.Lit(ch[:]))
		running += 8
		idx := len(lay.chunks)
		lay.chunks = append(lay.chunks, chunk{id: oc.id, bodyOff: running, bodyLen: oc.bodyLen})
		if oc.text != nil {
			lay.texts = append(lay.texts, *oc.text)
		}
		switch oc.role {
		case roleID3:
			lay.id3Idx = idx
		case roleSSND:
			lay.ssndIdx = idx
			lay.audioEnd = running + oc.bodyLen
			// Match parse: skip the SSND "offset" alignment bytes in the result view.
			// Metadata edits copy the SSND verbatim, so the source's ssndAlign still applies.
			lay.audioOff = min(soundDataStart(running, oc.bodyLen)+d.ssndAlign, lay.audioEnd)
		}
		if oc.body != nil {
			segs = append(segs, bits.Lit(oc.body))
		} else {
			segs = append(segs, bits.Copy(oc.srcOff, oc.bodyLen))
		}
		running += oc.bodyLen
		if oc.bodyLen&1 == 1 {
			// Word-alignment pad, always a literal zero: IFF defines pad bytes as zero and
			// not part of the data, and a malformed source may omit the final chunk's pad,
			// so copying it would read past EOF.
			segs = append(segs, bits.Lit([]byte{0}))
			running++
		}
	}
	if d.trailingLen > 0 {
		segs = append(segs, bits.Copy(d.trailingOff, d.trailingLen))
	}
	if d.outerLen > 0 {
		segs = append(segs, bits.Copy(d.outerOff, d.outerLen)) // appended after the FORM chunk
	}
	return segs, lay, nil
}

// buildResult constructs the plan's result. Its canonical view
// is re-projected from the written containers with the same project Parse uses, so it
// equals a fresh parse of the output.
func buildResult(edited *core.Media, base *doc, newID3 *id3.Tag, lay outLayout) *core.Media {
	// The track bounds the chapter ends, so derive it from the written chunks as Parse does:
	// the written SSND declares the bytes it holds.
	written := &doc{track: buildTrack(base.comm, ssndAudioBytes(lay.chunks, lay.ssndIdx, base.ssndAlign))}
	written.texts = lay.texts
	if lay.id3Idx >= 0 {
		written.id3 = newID3
	}
	m := &core.Media{
		Format:     core.FormatAIFF,
		Properties: edited.Properties.Clone(),
		AudioStart: lay.audioOff,
		AudioEnd:   lay.audioEnd,
	}
	project(written, m)
	return m
}
