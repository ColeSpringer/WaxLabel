package wav

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan builds a preservation-first rewrite: keep chunk order, copy data verbatim,
// re-render tag containers, recompute RIFF size. Tag precedence: see package doc.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("wav: edited media has no WAV native document")
	}

	infoPresent := d.infoIdx >= 0
	id3Present := d.id3 != nil

	tagsChanged := !base.Tags.Equal(edited.Tags) || !core.EqualTextItems(base, edited)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	// LegacyStrip consolidates tags into the id3 chunk: it drops LIST/INFO and never creates one.
	legacyStrip := opts.Legacy == core.LegacyStrip
	stripINFO := legacyStrip && infoPresent
	// Keys whose INFO items this write re-renders: those whose value moved plus those the
	// edit named outright. Setting the already-projected value is how a caller resolves
	// an INFO item the id3 chunk disagrees with. Track number and total share one item.
	changed := foldInfoTrackPair(core.ChangedKeys(base.Tags, edited.Tags, opts.Touched))

	// A WithStripEncoderStamp edit removes a transcoder-stamp ISFT item.
	encoderAuthored := changed[tag.Encoder]
	stripISFT := opts.StripEncoderStamp && !encoderAuthored
	stampToStrip := stripISFT && infoPresent && hasTranscoderISFT(d.info)

	report := core.WriteReport{Format: core.FormatWAV, BytesBefore: edited.Identity.Size}

	// One WriteOpts for both the predicate and the rebuild, so they cannot disagree.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// A requested write encoding (--numeric-genre) changes how a value is stored, not the
	// value itself, so the tag comparison above cannot see it.
	encodingRewrite := id3.EncodingRewriteNeeded(d.id3, edited.Tags, wopts)

	// Decide which containers receive the edited tags. Chapters, synced lyrics and a comment's
	// or lyrics' description and language force an id3 chunk because LIST/INFO cannot store
	// them (they are ID3 CHAP/CTOC and SYLT frames and COMM/USLT fields).
	needID3 := id3Present || len(edited.Pictures) > 0 || len(edited.Chapters) > 0 || len(edited.SyncedLyrics) > 0 ||
		id3.NeedsTextFrames(edited) || !infoRepresentable(edited.Tags, changed) || legacyStrip
	writeINFO := (infoPresent && !stripINFO) || !needID3

	// Build the new INFO items (changed keys re-rendered; everything else kept verbatim).
	var newInfo []infoItem
	if writeINFO {
		newInfo = rebuildInfo(d.info, edited.Tags, changed, stripISFT)
	}
	// An explicit set of a key whose INFO item disagreed with the projection changes INFO
	// while leaving the canonical set as it was; it must defeat both no-op gates.
	infoRewrite := writeINFO && infoPresent && !equalInfoItems(newInfo, d.info)

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy flagged NoOp, so
	// SaveAsFile/WriteTo still produce a whole file and SaveBack skips it. A chapters-
	// or synced-lyrics-only edit must defeat the gate too.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !stripINFO && !stampToStrip && !encodingRewrite && !infoRewrite {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// Re-check the ID3 CTOC count at the codec boundary. Only a chapter edit re-renders
	// the CTOC, so unchanged chapters can keep their source frames.
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	// id3.RewriteBase picks the diff base: empty with no id3 chunk, the parsed id3
	// frames alone under --legacy strip (so INFO-only values are emitted into id3),
	// and the merged base otherwise.
	var newID3 *id3.Tag
	var id3Info id3.RebuildInfo
	if needID3 {
		srcTag := d.id3
		if srcTag == nil {
			srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatWAV))
		}
		version := srcTag.WriteVersion()
		id3Base := id3.RewriteBase(base.Tags, srcTag, id3Present, stripINFO)
		var frames []id3.Frame
		frames, id3Info = id3.RebuildFrames(srcTag.Frames(), id3Base, id3Tags(edited.Tags, id3Present, encoderAuthored), version,
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

	// An empty container is not emitted (no point writing a header-only INFO or
	// id3 tag); this also lets a full clear drop the container.
	emitINFO := writeINFO && len(newInfo) > 0
	emitID3 := needID3 && newID3 != nil && len(newID3.Frames()) > 0
	// A container the edit leaves alone is copied whole: a LIST/INFO with the tail its item walk
	// could not read, an id3 chunk with its padding.
	infoKept := writeINFO && infoPresent && !infoRewrite
	id3Kept := id3.ChunkKept(d.id3, newID3)
	if id3Kept {
		newID3 = d.id3
	}

	// When both LIST/INFO and ID3 are emitted, a multi-valued key keeps its full set only
	// in ID3.
	if emitINFO && emitID3 {
		report.Warnings = append(report.Warnings, nativeReducedWarnings(edited.Tags, changed)...)
	}

	outs, ops, dropped, staleDropped := planChunks(d, newInfo, newID3, emitINFO, emitID3, infoKept, id3Kept, stripINFO)

	segs, lay, err := assemble(d, outs)
	if err != nil {
		return nil, err
	}
	report.Operations = ops
	// A conflict is resolved only in a chunk the output holds, whichever side the edit moved;
	// a strip or a full clear drops the LIST outright.
	if emitINFO {
		for _, k := range infoConflictKeys(base.Families, changed) {
			report.Operations = append(report.Operations, "LIST/INFO conflict resolved ("+string(k)+")")
		}
	}
	if emitID3 {
		// The embedded-container op lines (pictures/chapters/synced lyrics) come from the shared
		// id3.ContainerOps, which owns the change-flag-and-count gate.
		report.Operations = append(report.Operations, id3.ContainerOps(
			picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters),
			syncedLyricsChanged, len(edited.SyncedLyrics))...)
	}
	if stripINFO {
		// LegacyStrip moves the mapped items into the id3 chunk. A value the projection did not
		// select and an item with no canonical key have no frame to move into, so dropping the
		// chunk destroys them. doc.go requires a warning for such a loss.
		var lost []string
		keys := core.StrippedFamilyKeys(base.Families, core.FamilyRIFF, edited.Tags)
		if len(keys) > 0 {
			lost = append(lost, core.StrippedValuesLost(keys))
		}
		if ids := unmappedInfoIDs(d.info); len(ids) > 0 {
			lost = append(lost, "items no canonical key can hold ("+strings.Join(ids, ", ")+")")
		}
		if len(lost) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnLegacyStripDropped,
				core.StripDroppedMessage("LIST/INFO chunk", lost), keys...)
		}
	}
	if d.infoTail > 0 && !infoKept {
		// rebuildInfo re-renders the chunk from the items alone, so bytes the parser could
		// not read as items are lost. Under --legacy strip this fires alongside
		// legacy-strip-dropped; the two losses are distinct.
		report.Warnings = core.Warn(report.Warnings, core.WarnMalformedTagEntryDropped,
			fmt.Sprintf("%d byte(s) of the LIST/INFO chunk could not be read as items and are not carried into the rewritten chunk", d.infoTail))
	}
	if stampToStrip {
		// Report the strip even when it empties the LIST, which records no rewrite op.
		report.Operations = append(report.Operations, "ISFT encoder stamp strip")
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

	result := buildResult(edited, d, newInfo, newID3, lay)
	var written []id3.Frame
	if emitID3 {
		written = newID3.Frames()
	}
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "tag chunk", base.Tags, result.Tags,
		droppedLosses(dropped, id3.NewDupGrader(base, result, d.id3.Frames(), written)))
	if staleDropped {
		report.Warnings = core.Warn(report.Warnings, core.WarnMalformedTagEntryDropped,
			"the id3 chunk that could not be read is replaced by the rewritten one and its contents are not carried")
	}
	// Report ID3 rebuild losses only when the file as a whole loses them: RecordingDate
	// can survive verbatim in LIST/INFO ICRD, and the helper checks the re-projected output.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, id3Info, result.Tags)
	if !id3Kept {
		report.Warnings = id3.AppendChunkDropped(report.Warnings, d.id3, emitID3)
	}
	// Collapse to a no-op when the containers re-projected to base's values (a numeric
	// genre, a dropped empty). DowngradeNoOp carries the value-dropped warning forward.
	if np := core.DowngradeNoOp(core.FormatWAV, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), stripINFO || stampToStrip || encodingRewrite || infoRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// id3Tags is the set the id3 chunk renders from: edited, minus an inherited transcoder
// stamp when this write creates the chunk and the edit did not author the stamp.
func id3Tags(edited tag.TagSet, id3Present, encoderAuthored bool) tag.TagSet {
	if id3Present || encoderAuthored {
		return edited
	}
	vals, ok := edited.Get(tag.Encoder)
	if !ok {
		return edited
	}
	clean := make([]string, 0, len(vals))
	for _, v := range vals {
		if !core.IsTranscoderStamp(v) {
			clean = append(clean, v)
		}
	}
	if len(clean) == len(vals) {
		return edited
	}
	out := edited.Clone()
	if len(clean) == 0 {
		out.Delete(tag.Encoder)
	} else {
		out.Set(tag.Encoder, clean...)
	}
	return out
}

// planChunks builds the output chunk list in source order: each tag container kept whole,
// re-rendered where it stands or dropped, and everything else (including the data chunk) copied
// verbatim; a newly created container goes before the data chunk. It returns the duplicate tag
// chunks the rewrite drops and whether it drops a stale id3 chunk, one that could not be read,
// that a fresh one replaces.
func planChunks(d *doc, newInfo []infoItem, newID3 *id3.Tag, emitINFO, emitID3, infoKept, id3Kept, stripINFO bool) (outs []outChunk, ops []string, dropped []chunk, staleDropped bool) {
	infoPlaced, id3Placed := false, false

	for i, ch := range d.chunks {
		switch i {
		case d.infoIdx:
			switch {
			case stripINFO:
				ops = append(ops, "LIST/INFO strip")
			case infoKept:
				outs = append(outs, outChunk{id: ch.id, role: roleINFO, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
			case emitINFO:
				outs = append(outs, infoOut(newInfo))
				ops = append(ops, "LIST/INFO rewrite")
			default:
				// The edit emptied the chunk, so Execute deletes one the file had.
				ops = append(ops, "LIST/INFO drop")
			}
			infoPlaced = emitINFO
		case d.id3Idx:
			switch {
			case id3Kept:
				outs = append(outs, outChunk{id: ch.id, role: roleID3, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
			case emitID3:
				outs = append(outs, id3Out(newID3, ch.id, d.id3, ch.bodyLen))
				ops = append(ops, "id3 chunk rewrite")
			default:
				ops = append(ops, "id3 chunk drop")
			}
			id3Placed = emitID3
		default:
			if ch.dupTag {
				// A second LIST/INFO or id3 chunk: dropped so the output carries one copy.
				ops = append(ops, "duplicate tag chunk drop")
				dropped = append(dropped, ch)
				continue
			}
			// A lone id3 chunk that failed to parse is not marked dupTag. Drop it when a
			// fresh id3 chunk is written, so the output never carries two.
			if emitID3 && isID3Chunk(ch.id4()) {
				ops = append(ops, "stale id3 chunk drop")
				staleDropped = true
				continue
			}
			role := roleOther
			if i == d.dataIdx {
				role = roleData
			}
			outs = append(outs, outChunk{id: ch.id, role: role, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
		}
	}

	// Insert newly created containers (INFO then id3) just before the data chunk,
	// the conventional, always-read position.
	var created []outChunk
	if emitINFO && !infoPlaced {
		created = append(created, infoOut(newInfo))
		ops = append(ops, "LIST/INFO creation")
	}
	if emitID3 && !id3Placed {
		created = append(created, id3Out(newID3, [4]byte{'i', 'd', '3', ' '}, nil, 0))
		ops = append(ops, "id3 chunk creation")
	}
	if len(created) > 0 {
		outs = insertBeforeData(outs, created)
	}
	return outs, ops, dropped, staleDropped
}

// droppedLosses says what dropping each of the dropped tag chunks loses: a LIST/INFO duplicate its
// values, an id3 chunk what g finds.
func droppedLosses(dropped []chunk, g *id3.DupGrader) []core.DuplicateContent {
	out := make([]core.DuplicateContent, len(dropped))
	for i, ch := range dropped {
		if !isID3Chunk(ch.id4()) {
			out[i] = ch.dupContent
			continue
		}
		out[i] = g.Loss(ch.dupID3, "unreadable id3 chunk(s)")
	}
	return out
}

// infoOut builds the LIST/INFO output chunk from rendered INFO items.
func infoOut(items []infoItem) outChunk {
	body := renderInfo(items)
	return outChunk{id: [4]byte{'L', 'I', 'S', 'T'}, role: roleINFO, body: body, bodyLen: int64(len(body))}
}

// id3Out builds the id3 output chunk under id from t, rendered in place of src, the tag the chunk
// held in srcLen bytes (nil for a new chunk).
func id3Out(t *id3.Tag, id [4]byte, src *id3.Tag, srcLen int64) outChunk {
	body := id3.RenderChunk(t, src, srcLen)
	return outChunk{id: id, role: roleID3, body: body, bodyLen: int64(len(body))}
}

// insertBeforeData inserts created chunks just before the data chunk, or at the
// end when there is no data chunk.
func insertBeforeData(outs, created []outChunk) []outChunk {
	for i, oc := range outs {
		if oc.role == roleData {
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
// containers without guessing from identifiers.
type chunkRole uint8

const (
	roleOther chunkRole = iota
	roleINFO
	roleID3
	roleData
)

// outChunk is one chunk in the planned output: a literal body (re-rendered or
// created) or a verbatim copy from the source.
type outChunk struct {
	id      [4]byte
	role    chunkRole
	body    []byte // literal body; nil means copy bodyLen bytes from srcOff
	srcOff  int64
	bodyLen int64
}

// outLayout is the byte-level result of assembling the output chunks: the tag
// containers the output holds, the data chunk's new body offset, and the total
// output size.
type outLayout struct {
	dataOff int64
	total   int64
	hasINFO bool
	hasID3  bool
}

// assemble turns the output chunks into a rewrite segment list and recomputes the
// container size, returning the layout needed to build the plan's result.
func assemble(d *doc, outs []outChunk) (segs []bits.Segment, lay outLayout, err error) {
	rf64 := d.isRF64()
	if rf64 {
		outs = stripDS64(outs)
	}
	var chunksTotal int64
	for _, oc := range outs {
		if oc.bodyLen > math.MaxUint32 && !rf64 {
			return nil, lay, fmt.Errorf("%w: chunk %q body is %d bytes (max %d)",
				waxerr.ErrSizeTooLarge, string(oc.id[:]), oc.bodyLen, int64(math.MaxUint32))
		}
		chunksTotal += 8 + oc.bodyLen + (oc.bodyLen & 1)
	}
	chunksTotal += d.trailingLen

	var ds64Body []byte
	if rf64 {
		// The ds64 body length is known before the container size (its table depends only
		// on the output chunks' lengths), so the size it reports can include its own chunk.
		table, dataSize := ds64Overrides(outs)
		chunksTotal += 8 + int64(ds64MinBody+len(table)*ds64Entry)
		ds64Body = renderDS64(uint64(4+chunksTotal), dataSize, d.ds64.sampleCount, table)
		outs = append([]outChunk{{
			id: [4]byte{'d', 's', '6', '4'}, body: ds64Body, bodyLen: int64(len(ds64Body)),
		}}, outs...)
	}

	riffSize := 4 + chunksTotal // "WAVE" + all chunks (in-container trailing included)
	// Out-of-container trailing is appended after the RIFF chunk, not counted in its
	// size, so a strict reader walking by that size does not misparse it.
	lay.total = 8 + riffSize + d.outerLen
	if riffSize > math.MaxUint32 && !rf64 {
		return nil, lay, fmt.Errorf("%w: WAV output is %d bytes, exceeding the 4 GiB RIFF limit (use RF64)",
			waxerr.ErrSizeTooLarge, lay.total)
	}

	var head [12]byte
	copy(head[0:4], d.headerID())
	binary.LittleEndian.PutUint32(head[4:8], sizeField(riffSize, rf64))
	copy(head[8:12], "WAVE")
	segs = append(segs, bits.Lit(head[:]))

	running := int64(12)
	for _, oc := range outs {
		var ch [8]byte
		copy(ch[0:4], oc.id[:])
		// In RF64 the data chunk's size field is always the marker, whether or not the
		// value would fit, matching what RF64 writers emit and what the ds64 chunk is for.
		binary.LittleEndian.PutUint32(ch[4:8], sizeField(oc.bodyLen, rf64 && (oc.role == roleData || oc.bodyLen > math.MaxUint32)))
		segs = append(segs, bits.Lit(ch[:]))
		running += 8
		switch oc.role {
		case roleINFO:
			lay.hasINFO = true
		case roleID3:
			lay.hasID3 = true
		case roleData:
			lay.dataOff = running
		}
		if oc.body != nil {
			segs = append(segs, bits.Lit(oc.body))
		} else {
			segs = append(segs, bits.Copy(oc.srcOff, oc.bodyLen))
		}
		running += oc.bodyLen
		if oc.bodyLen&1 == 1 {
			// Word-alignment pad, always a literal zero: RIFF defines pad bytes as zero
			// and not part of the data, and a malformed source may omit the final chunk's
			// pad, so copying it would read past EOF.
			segs = append(segs, bits.Lit([]byte{0}))
			running++
		}
	}
	if d.trailingLen > 0 {
		segs = append(segs, bits.Copy(d.trailingOff, d.trailingLen))
	}
	if d.outerLen > 0 {
		segs = append(segs, bits.Copy(d.outerOff, d.outerLen)) // appended after the RIFF chunk
	}
	return segs, lay, nil
}

// sizeField renders a 32-bit chunk-size field, substituting the RF64 marker when the
// real size lives in the ds64 chunk instead.
func sizeField(n int64, marked bool) uint32 {
	if marked {
		return rf64Marker
	}
	return uint32(n)
}

// headerID is the container's 12-byte header id, defaulting to "RIFF" for a document
// built without one (a synthesized result, or a zero-value doc).
func (d *doc) headerID() string {
	if d.form == ([4]byte{}) {
		return "RIFF"
	}
	return string(d.form[:])
}

// stripDS64 removes the source ds64 chunk from the output list; it is regenerated
// from the new layout because a metadata rewrite moves the sizes it carries.
func stripDS64(outs []outChunk) []outChunk {
	kept := outs[:0:0]
	for _, oc := range outs {
		if string(oc.id[:]) != "ds64" {
			kept = append(kept, oc)
		}
	}
	return kept
}

// ds64Overrides derives the ds64 chunk's data size and chunk-size table from the
// output chunks: the data chunk's real length, plus one table entry per other chunk
// whose length no longer fits a 32-bit field.
func ds64Overrides(outs []outChunk) (table []ds64Size, dataSize uint64) {
	for _, oc := range outs {
		switch {
		case oc.role == roleData && dataSize == 0:
			dataSize = uint64(oc.bodyLen)
		case oc.bodyLen > math.MaxUint32:
			table = append(table, ds64Size{id: oc.id, size: uint64(oc.bodyLen)})
		}
	}
	return table, dataSize
}

// buildResult constructs the plan's result. Its canonical view
// is re-projected from the written containers with the same project Parse uses, so it
// equals a fresh parse of the output.
func buildResult(edited *core.Media, base *doc, newInfo []infoItem, newID3 *id3.Tag, lay outLayout) *core.Media {
	// The data and fmt chunks are copied verbatim, so the track holds.
	written := &doc{track: base.track}
	if lay.hasINFO {
		written.info = newInfo
	}
	if lay.hasID3 {
		written.id3 = newID3
	}
	m := &core.Media{
		Format:     core.FormatWAV,
		Properties: edited.Properties.Clone(),
		AudioStart: lay.dataOff,
		AudioEnd:   lay.dataOff + base.dataLen,
	}
	project(written, m)
	return m
}
