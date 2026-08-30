package wav

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan computes the byte-level rewrite that turns the original WAV into the
// edited media. It is preservation-first: every chunk is kept in order and
// copied verbatim except the tag containers, the audio "data" chunk is copied
// byte-for-byte, and the RIFF size is recomputed.
//
// Two tag containers are reconciled by the precedence policy (see the package
// doc): the embedded id3 chunk holds pictures and the full canonical set; the
// RIFF-native LIST/INFO holds the representable subset so the ffmpeg family
// still reads the file. Both present containers are written from the same edited
// set, so they end up in agreement; a value INFO cannot represent (multi-value,
// an unmapped key, or any picture) forces an id3 chunk so nothing is lost.
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

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	// LegacyStrip consolidates tags into the id3 chunk by dropping LIST/INFO.
	stripINFO := opts.Legacy == core.LegacyStrip && infoPresent
	// A WithStripEncoderStamp edit removes a transcoder-stamp ISFT item. The strip targets
	// the stamp the FILE carries, never a value this edit authored: the CLI turns the option
	// on for any ENCODER edit (to keep the containers in step), so without this gate
	// --set ENCODER=Lavf62.3.100 would filter out the user's own value and write it nowhere,
	// ISFT being the INFO home for the key. A removal leaves no value to write either way.
	//
	// A strip is a real change even when the canonical tags are untouched (a WAV carrying
	// only an inherited ISFT, or one whose id3 chunk holds a clean ENCODER while INFO holds
	// the stamp), so it must defeat the no-op fast path below and force an INFO rewrite.
	encoderAuthored := core.DiffKeys(base.Tags, edited.Tags)[tag.Encoder]
	stripISFT := opts.StripEncoderStamp && !encoderAuthored
	stampToStrip := stripISFT && infoPresent && hasTranscoderISFT(d.info)

	report := core.WriteReport{Format: core.FormatWAV, BytesBefore: edited.Identity.Size}

	// One WriteOpts for both the predicate and the rebuild: they must render from identical
	// options, or the predicate could green-light a write the rebuild then renders differently.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// A requested write encoding (--numeric-genre) changes how a value is stored, not the
	// value itself, so the tag comparison above cannot see it. It reaches only the id3
	// chunk: LIST/INFO IGNR stores the genre name literally, and a file with no id3 chunk
	// passes a nil tag, for which the predicate is false.
	encodingRewrite := id3.EncodingRewriteNeeded(d.id3, edited.Tags, wopts)

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it. A
	// chapters- or synced-lyrics-only edit (CHAP/CTOC, SYLT in the id3 chunk) must defeat
	// the gate too.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !stripINFO && !stampToStrip && !encodingRewrite {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// Re-check the ID3 CTOC count at the codec boundary. Only a chapter edit re-renders
	// the CTOC, so unchanged chapters can keep their source frames.
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	// Decide which containers receive the edited tags. Chapters and synced lyrics force an
	// id3 chunk because LIST/INFO cannot store them (they are ID3 CHAP/CTOC and SYLT frames).
	needID3 := id3Present || len(edited.Pictures) > 0 || len(edited.Chapters) > 0 || len(edited.SyncedLyrics) > 0 || !infoRepresentable(edited.Tags) || stripINFO
	writeINFO := (infoPresent && !stripINFO) || !needID3

	// Build the new INFO items (synced to the edited set; unmapped items kept).
	var newInfo []infoItem
	if writeINFO {
		newInfo = rebuildInfo(d.info, edited.Tags, stripISFT)
	}

	// Build the new id3 tag. id3.RewriteBase picks the diff base: no id3 chunk
	// means an empty base, --legacy strip uses only the parsed id3 frames so
	// INFO-only values are emitted into id3, and the default path uses the merged
	// base.
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
		newID3 = srcTag.WithFrames(frames, 0) // the embedded id3 chunk is rendered without padding
	}

	// An empty container is not emitted (no point writing a header-only INFO or
	// id3 tag); this also lets a full clear drop the container.
	emitINFO := writeINFO && len(newInfo) > 0
	emitID3 := needID3 && newID3 != nil && len(newID3.Frames()) > 0

	// When both LIST/INFO and ID3 are emitted, a multi-valued key keeps its full set
	// only in ID3; INFO stores just the first value. Surface that native reduction as
	// a plan-time note. Gate on the emit flags, not needID3/writeINFO, because a full
	// clear can leave writeINFO true yet emit no INFO chunk.
	if emitINFO && emitID3 {
		report.Warnings = append(report.Warnings, nativeReducedWarnings(edited.Tags)...)
	}

	outs, ops, dupLost := planChunks(d, newInfo, newID3, emitINFO, emitID3, stripINFO)

	segs, lay, err := assemble(d, outs)
	if err != nil {
		return nil, err
	}
	report.Operations = ops
	if emitID3 {
		// The embedded-container op lines (pictures/chapters/synced lyrics) come from the shared
		// id3.ContainerOps, which owns the change-flag-and-count gate.
		report.Operations = append(report.Operations, id3.ContainerOps(
			picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters),
			syncedLyricsChanged, len(edited.SyncedLyrics))...)
	}
	if stripINFO {
		// LegacyStrip consolidates the mapped items into the id3 chunk, but an unmapped item
		// has no canonical key and so no frame to move into: dropping the chunk destroys it.
		// doc.go's contract is that unaffected data is warned about, never stripped silently,
		// and this is the one WAV path that would. (AIFF is safe by construction: it only
		// collects text chunks that map, so its strip moves every one of them.)
		if ids := unmappedInfoIDs(d.info); len(ids) > 0 {
			report.Warnings = core.Warn(report.Warnings, core.WarnLegacyStripDropped,
				core.StripDroppedMessage("LIST/INFO chunk", []string{"items no canonical key can hold (" + strings.Join(ids, ", ") + ")"}))
		}
	}
	if d.infoIdx >= 0 && d.infoTail > 0 {
		// rebuildInfo re-renders the chunk from the items alone, so a region the parser could
		// not read as items has nowhere to go. Under --legacy strip this fires alongside
		// legacy-strip-dropped: two distinct losses, items with no canonical key and bytes
		// that were never items. A true no-op never reaches here, and core.DowngradeNoOp does
		// not carry this code, so an unchanged file stays quiet.
		report.Warnings = core.Warn(report.Warnings, core.WarnMalformedTagEntryDropped,
			fmt.Sprintf("%d byte(s) of the LIST/INFO chunk could not be read as items and are not carried into the rewritten chunk", d.infoTail))
	}
	if stampToStrip {
		// Surface the strip even when it empties the LIST (which records no rewrite op),
		// so a plan that only drops the stamp is not reported as a contentless rewrite.
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

	result := buildResult(edited, d, newInfo, newID3, lay)
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "tag chunk", result.Tags, dupLost)
	// Surface ID3 rebuild losses only when the file as a whole loses them. WAV also writes
	// RecordingDate to native LIST/INFO ICRD, where it can survive verbatim; the shared
	// helper checks the re-projected output before warning.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, id3Info, result.Tags)
	report.Warnings = id3.AppendMalformedTailDropped(report.Warnings, d.id3)
	// Collapse to a true no-op when the containers re-projected to base's values
	// (a numeric genre, a dropped empty); an INFO strip, an encoder-stamp removal, or an
	// encoding rewrite stays a real write. DowngradeNoOp carries the value-dropped warning
	// forward so a dropped date still surfaces on a no-op.
	if np := core.DowngradeNoOp(core.FormatWAV, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), stripINFO || stampToStrip || encodingRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// id3Tags is the set the embedded id3 chunk renders from: edited, minus an inherited
// transcoder stamp when this write is CREATING the chunk and the stamp is not something the
// edit authored.
//
// The stamp reaches the canonical set from the ISFT item, so without this a wholly unrelated
// edit that happens to force an id3 chunk (a DISCNUMBER, which INFO has no slot for) would
// copy ffmpeg's leftover into a second container and make WaxLabel manufacture a second copy
// of the noise it lints against. It is the same judgement the transfer policy already makes
// when it excludes ENCODER from a copy: a stamp describes this file's own audio, not the
// work, so it is preserved where it is and never propagated.
//
// An id3 chunk that already holds a stamped TSSE keeps it: refusing to author the stamp is
// not a licence to delete one the file came with, and the linter flags it either way.
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

// planChunks builds the output chunk list in source order, re-rendering or
// dropping the tag containers and copying everything else (including the data
// chunk) verbatim, then inserting any newly created tag container before the
// data chunk. dupLost collects the canonical keys the dropped duplicate containers held and
// the surviving one does not, so the caller can warn about the values this write destroys.
func planChunks(d *doc, newInfo []infoItem, newID3 *id3.Tag, emitINFO, emitID3, stripINFO bool) (outs []outChunk, ops []string, dupLost []core.DuplicateContent) {
	infoRewritten, id3Rewritten := false, false

	for i, ch := range d.chunks {
		switch i {
		case d.infoIdx:
			if stripINFO {
				ops = append(ops, "LIST/INFO strip")
				continue
			}
			if emitINFO {
				outs = append(outs, infoOut(newInfo))
				infoRewritten = true
				ops = append(ops, "LIST/INFO rewrite")
				continue
			}
			continue // INFO present but now empty: drop it
		case d.id3Idx:
			if emitID3 {
				outs = append(outs, id3Out(newID3))
				id3Rewritten = true
				ops = append(ops, "id3 chunk rewrite")
				continue
			}
			continue // id3 present but now empty: drop it
		default:
			if ch.dupTag {
				// Redundant duplicate tag container (a second LIST/INFO or id3 chunk).
				// Drop it on rewrite so the output carries a single, consistent copy
				// rather than a stale shadow of the authoritative one.
				ops = append(ops, "duplicate tag chunk drop")
				dupLost = append(dupLost, ch.dupContent)
				continue
			}
			// A lone id3 chunk whose body failed to parse leaves no authoritative id3
			// (so it was not marked dupTag). Drop it when we are writing a fresh id3
			// chunk, so the output never carries two id3 chunks (which a re-parse would
			// flag as a duplicate, disagreeing with the returned document).
			if emitID3 && isID3Chunk(ch.id4()) {
				ops = append(ops, "stale id3 chunk drop")
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
	if emitINFO && !infoRewritten {
		created = append(created, infoOut(newInfo))
		ops = append(ops, "LIST/INFO creation")
	}
	if emitID3 && !id3Rewritten {
		created = append(created, id3Out(newID3))
		ops = append(ops, "id3 chunk creation")
	}
	if len(created) > 0 {
		outs = insertBeforeData(outs, created)
	}
	return outs, ops, dupLost
}

// infoOut builds the LIST/INFO output chunk from rendered INFO items.
func infoOut(items []infoItem) outChunk {
	body := renderInfo(items)
	return outChunk{id: [4]byte{'L', 'I', 'S', 'T'}, role: roleINFO, body: body, bodyLen: int64(len(body))}
}

// id3Out builds the "id3 " output chunk from a rendered ID3v2 tag.
func id3Out(t *id3.Tag) outChunk {
	body := id3.Render(t.WriteVersion(), t.Frames(), 0)
	return outChunk{id: [4]byte{'i', 'd', '3', ' '}, role: roleID3, body: body, bodyLen: int64(len(body))}
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

// chunkRole tags an output chunk so the result document can re-find the
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

// outLayout is the byte-level result of assembling the output chunks: the new
// chunk list (with output offsets), the container indices for the result
// document, the data chunk's new body offset, and the total output size.
type outLayout struct {
	chunks  []chunk
	dataOff int64
	total   int64
	infoIdx int
	id3Idx  int
	dataIdx int
}

// assemble turns the output chunks into a rewrite segment list and recomputes
// the container size, returning the layout needed to build the post-write
// document. An RF64/BW64 source keeps its form: the 32-bit size fields that
// cannot carry the value stay at the 0xFFFFFFFF marker and the regenerated ds64
// chunk carries the real ones, so a rewrite never silently downgrades a 64-bit
// file to plain RIFF (which would truncate its sizes).
func assemble(d *doc, outs []outChunk) (segs []bits.Segment, lay outLayout, err error) {
	lay = outLayout{infoIdx: -1, id3Idx: -1, dataIdx: -1}
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
	lay.chunks = make([]chunk, 0, len(outs))
	for _, oc := range outs {
		var ch [8]byte
		copy(ch[0:4], oc.id[:])
		// In RF64 the data chunk's size field is always the marker, whether or not the
		// value would fit, matching what RF64 writers emit and what the ds64 chunk is for.
		binary.LittleEndian.PutUint32(ch[4:8], sizeField(oc.bodyLen, rf64 && (oc.role == roleData || oc.bodyLen > math.MaxUint32)))
		segs = append(segs, bits.Lit(ch[:]))
		running += 8
		idx := len(lay.chunks)
		lay.chunks = append(lay.chunks, chunk{id: oc.id, bodyOff: running, bodyLen: oc.bodyLen})
		switch oc.role {
		case roleINFO:
			lay.infoIdx = idx
		case roleID3:
			lay.id3Idx = idx
		case roleData:
			lay.dataIdx = idx
			lay.dataOff = running
		}
		if oc.body != nil {
			segs = append(segs, bits.Lit(oc.body))
		} else {
			segs = append(segs, bits.Copy(oc.srcOff, oc.bodyLen))
		}
		running += oc.bodyLen
		if oc.bodyLen&1 == 1 {
			// Word-alignment pad. Always a literal zero: the RIFF spec defines pad
			// bytes as zero and not part of the data, and a malformed source may
			// omit the final chunk's pad entirely (so copying it would read past
			// EOF - found by the fuzzer).
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

// stripDS64 removes the source ds64 chunk from the output list. It is regenerated
// from the new layout rather than copied, since the sizes it carries are exactly
// what a metadata rewrite moves.
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

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing. Its canonical view is re-projected (via the same
// project used by Parse) from the containers actually written, so it equals a
// fresh parse of the output.
func buildResult(edited *core.Media, base *doc, newInfo []infoItem, newID3 *id3.Tag, lay outLayout) *core.Media {
	nd := &doc{
		chunks:  lay.chunks,
		infoIdx: lay.infoIdx,
		id3Idx:  lay.id3Idx,
		dataIdx: lay.dataIdx,
		dataOff: lay.dataOff,
		dataLen: base.dataLen,
		fmtCfg:  base.fmtCfg,
		// The audio and its fact chunk are copied verbatim, so the declared sample
		// count still describes the output.
		factSamples: base.factSamples,
		hasFact:     base.hasFact,
		track:       base.track,
		size:        lay.total,
		form:        base.form,
		ds64:        base.ds64.clone(),
	}
	if nd.ds64 != nil {
		// Keep the carried ds64 in step with the bytes just written, so the result document
		// describes the output rather than the source it was derived from.
		nd.ds64.riffSize = uint64(lay.total - 8 - base.outerLen)
		nd.ds64.dataSize = uint64(base.dataLen)
	}
	if lay.infoIdx >= 0 {
		nd.info = newInfo
	}
	if lay.id3Idx >= 0 {
		nd.id3 = newID3
	}
	// The in-RIFF trailing and out-of-RIFF regions were appended verbatim at the
	// end of the output, in that order; record their new offsets so re-editing the
	// returned document (without re-parsing) still preserves them.
	nd.outerLen = base.outerLen
	nd.outerOff = lay.total - base.outerLen
	nd.trailingLen = base.trailingLen
	nd.trailingID3v1 = base.trailingID3v1
	nd.trailingOff = nd.outerOff - base.trailingLen

	tags, pics, chapters, syncedLyrics, families, numericGenre, projWs := project(nd)
	return &core.Media{
		Format:       core.FormatWAV,
		Properties:   edited.Properties.Clone(),
		Tags:         tags,
		Pictures:     pics,
		Chapters:     chapters,
		SyncedLyrics: syncedLyrics,
		Families:     families,
		// Recompute warnings from the written containers so the returned document
		// matches a fresh parse of the output: a dropped duplicate no longer warns,
		// a resolved numeric genre no longer warns, and a preserved ISFT stamp still
		// does. (Duplicate-tag-block warnings are structural to the source and gone
		// once consolidated, so they are correctly absent here.) projWs carries the
		// id3-chunk chapter-flatten and synced-lyrics notes, re-derived from the written
		// frames like Parse.
		Warnings:   append(projWs, mediaWarnings(nd, numericGenre)...),
		Native:     nd,
		Identity:   core.Identity{Size: lay.total},
		AudioStart: lay.dataOff,
		AudioEnd:   lay.dataOff + base.dataLen,
	}
}
