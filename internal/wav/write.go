package wav

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

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
	// LegacyStrip consolidates tags into the id3 chunk by dropping LIST/INFO.
	stripINFO := opts.Legacy == core.LegacyStrip && infoPresent
	// A WithStripEncoderStamp edit removes a transcoder-stamp ISFT that no canonical
	// tag edit reaches (E1). It is a real change even when the canonical tags are
	// untouched (the #2 repro: a WAV carrying only an inherited ISFT), so it must
	// defeat the no-op fast path below and force an INFO rewrite.
	stampToStrip := opts.StripEncoderStamp && infoPresent && hasTranscoderISFT(d.info)

	report := core.WriteReport{Format: core.FormatWAV, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it.
	if !tagsChanged && !picturesChanged && !stripINFO && !stampToStrip {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// Decide which containers receive the edited tags.
	needID3 := id3Present || len(edited.Pictures) > 0 || !infoRepresentable(edited.Tags) || stripINFO
	writeINFO := (infoPresent && !stripINFO) || !needID3

	// Build the new INFO items (synced to the edited set; unmapped items kept).
	var newInfo []infoItem
	if writeINFO {
		newInfo = rebuildInfo(d.info, edited.Tags, opts.StripEncoderStamp)
	}

	// Build the new id3 tag. When no id3 existed, the rewrite base is empty so the
	// whole authoritative set (promoted from INFO plus the edit) renders into the
	// new chunk; when one existed, only changed frames are re-rendered.
	var newID3 *id3.Tag
	var id3Info id3.RebuildInfo
	if needID3 {
		srcTag := d.id3
		if srcTag == nil {
			srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatWAV))
		}
		version := srcTag.WriteVersion()
		id3Base := base.Tags
		if !id3Present {
			id3Base = tag.NewTagSet()
		}
		var frames []id3.Frame
		frames, id3Info = id3.RebuildFrames(srcTag.Frames(), id3Base, edited.Tags, version,
			edited.Pictures, picturesChanged, id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre})
		if err := id3.CheckSize(version, frames); err != nil {
			return nil, err
		}
		newID3 = srcTag.WithFrames(frames)
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

	outs, ops := planChunks(d, newInfo, newID3, emitINFO, emitID3, stripINFO)

	segs, lay, err := assemble(d, outs)
	if err != nil {
		return nil, err
	}
	report.Operations = ops
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
	report.BytesAfter = lay.total

	result := buildResult(edited, d, newInfo, newID3, lay)
	// Collapse to a true no-op when the containers re-projected to base's values
	// (a numeric genre, a dropped empty); an INFO strip or encoder-stamp removal stays
	// a real write. See core.DowngradeNoOp.
	if np := core.DowngradeNoOp(core.FormatWAV, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), stripINFO || stampToStrip); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// planChunks builds the output chunk list in source order, re-rendering or
// dropping the tag containers and copying everything else (including the data
// chunk) verbatim, then inserting any newly created tag container before the
// data chunk.
func planChunks(d *doc, newInfo []infoItem, newID3 *id3.Tag, emitINFO, emitID3, stripINFO bool) ([]outChunk, []string) {
	var outs []outChunk
	var ops []string
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
	if emitID3 {
		if n := id3.APICCount(newID3); n > 0 {
			ops = append(ops, fmt.Sprintf("pictures: %d", n))
		}
	}
	return outs, ops
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
// the RIFF size, returning the layout needed to build the post-write document.
func assemble(d *doc, outs []outChunk) (segs []bits.Segment, lay outLayout, err error) {
	lay = outLayout{infoIdx: -1, id3Idx: -1, dataIdx: -1}
	var chunksTotal int64
	for _, oc := range outs {
		if oc.bodyLen > math.MaxUint32 {
			return nil, lay, fmt.Errorf("%w: chunk %q body is %d bytes (max %d)",
				waxerr.ErrSizeTooLarge, string(oc.id[:]), oc.bodyLen, int64(math.MaxUint32))
		}
		chunksTotal += 8 + oc.bodyLen + (oc.bodyLen & 1)
	}
	chunksTotal += d.trailingLen
	riffSize := 4 + chunksTotal // "WAVE" + all chunks (in-RIFF trailing included)
	// Out-of-RIFF trailing is appended after the RIFF chunk, not counted in its
	// size, so a strict reader walking by the RIFF size does not misparse it.
	lay.total = 8 + riffSize + d.outerLen
	if riffSize > math.MaxUint32 {
		return nil, lay, fmt.Errorf("%w: WAV output is %d bytes, exceeding the 4 GiB RIFF limit (use RF64)",
			waxerr.ErrSizeTooLarge, lay.total)
	}

	var head [12]byte
	copy(head[0:4], "RIFF")
	binary.LittleEndian.PutUint32(head[4:8], uint32(riffSize))
	copy(head[8:12], "WAVE")
	segs = append(segs, bits.Lit(head[:]))

	running := int64(12)
	lay.chunks = make([]chunk, 0, len(outs))
	for _, oc := range outs {
		var ch [8]byte
		copy(ch[0:4], oc.id[:])
		binary.LittleEndian.PutUint32(ch[4:8], uint32(oc.bodyLen))
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
		track:   base.track,
		size:    lay.total,
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
	nd.trailingOff = nd.outerOff - base.trailingLen

	tags, pics, families, numericGenre := project(nd)
	return &core.Media{
		Format:     core.FormatWAV,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Families:   families,
		// Recompute warnings from the written containers so the returned document
		// matches a fresh parse of the output: a dropped duplicate no longer warns,
		// a resolved numeric genre no longer warns, and a preserved ISFT stamp still
		// does. (Duplicate-tag-block warnings are structural to the source and gone
		// once consolidated, so they are correctly absent here.)
		Warnings:   mediaWarnings(nd, numericGenre),
		Native:     nd,
		Identity:   core.Identity{Size: lay.total},
		AudioStart: lay.dataOff,
		AudioEnd:   lay.dataOff + base.dataLen,
	}
}
