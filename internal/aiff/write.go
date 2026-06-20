package aiff

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

// Plan computes the byte-level rewrite that turns the original AIFF into the
// edited media. It is preservation-first: every chunk is kept in order and
// copied verbatim except the tag containers, the SSND sound chunk is copied
// byte-for-byte, and the FORM size is recomputed.
//
// Two tag containers are reconciled by the precedence policy (see the package
// doc): the embedded "ID3 " chunk holds pictures and the full canonical set; the
// native text chunks (NAME/AUTH/"(c) "/ANNO) hold the representable subset so the
// ffmpeg family still reads the file. Both present containers are written from
// the same edited set, so they end up in agreement; a value the native chunks
// cannot represent (an unmapped key, a multi-value field other than Comment, or
// any picture) forces an "ID3 " chunk so nothing is lost.
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

	// Reconcile/UpdateExisting must migrate values between containers; that is
	// deferred (as for FLAC, MP3, and WAV), so fail loudly when there is a secondary
	// container to act on rather than silently doing nothing.
	if (opts.Legacy == core.LegacyReconcile || opts.Legacy == core.LegacyUpdateExisting) && textPresent && id3Present {
		return nil, fmt.Errorf("%w: legacy policy %q is not yet implemented for AIFF and both tag containers are present",
			waxerr.ErrUnsupportedTag, opts.Legacy)
	}

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	// LegacyStrip consolidates tags into the ID3 chunk by dropping the native ones.
	stripText := opts.Legacy == core.LegacyStrip && textPresent

	report := core.WriteReport{Format: core.FormatAIFF, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it.
	if !tagsChanged && !picturesChanged && !stripText {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// Decide which containers receive the edited tags.
	needID3 := id3Present || len(edited.Pictures) > 0 || !textRepresentable(edited.Tags) || stripText
	writeText := (textPresent && !stripText) || !needID3

	// Build the new native text chunks (synced to the edited set).
	var newText []outChunk
	if writeText {
		newText = rebuildText(d.texts, edited.Tags)
	}

	// Build the new ID3 tag. When no ID3 existed, the rewrite base is empty so the
	// whole authoritative set (promoted from the native chunks plus the edit)
	// renders into the new chunk; when one existed, only changed frames re-render.
	var newID3 *id3.Tag
	var id3Info id3.RebuildInfo
	if needID3 {
		srcTag := d.id3
		if srcTag == nil {
			srcTag = id3.NewEmpty(3)
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

	// An empty container is not emitted (no point writing a header-only chunk);
	// this also lets a full clear drop the container.
	emitText := writeText && len(newText) > 0
	emitID3 := needID3 && newID3 != nil && len(newID3.Frames()) > 0

	outs, ops := planChunks(d, newText, newID3, emitText, emitID3, stripText)

	segs, lay, err := assemble(d, outs)
	if err != nil {
		return nil, err
	}
	report.Operations = ops
	if id3Info.UsedV23Multi {
		report.Operations = append(report.Operations, "v2.3 multi-value NUL-separated storage")
		report.Warnings = core.Warn(report.Warnings, core.WarnID3MultiValue,
			"a multi-value field was written NUL-separated in ID3v2.3, a de-facto extension some readers do not split")
	}
	report.BytesAfter = lay.total

	result := buildResult(edited, d, newText, newID3, lay)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// planChunks builds the output chunk list in source order, re-rendering or
// dropping the tag containers and copying everything else (including the SSND
// sound chunk) verbatim. The native text chunks are regrouped at the position of
// the first one (their order among themselves is not significant); a newly
// created container is inserted before the SSND chunk.
func planChunks(d *doc, newText []outChunk, newID3 *id3.Tag, emitText, emitID3, stripText bool) ([]outChunk, []string) {
	var outs []outChunk
	var ops []string
	textGroupEmitted, id3Rewritten := false, false

	firstTextIdx := -1
	if len(d.textIdx) > 0 {
		firstTextIdx = d.textIdx[0]
	}
	textIdxSet := map[int]bool{}
	for _, i := range d.textIdx {
		textIdxSet[i] = true
	}

	for i, ch := range d.chunks {
		switch {
		case textIdxSet[i]:
			if stripText {
				if !textGroupEmitted {
					ops = append(ops, "native text chunk strip")
					textGroupEmitted = true
				}
				continue
			}
			// Regroup all native text chunks at the first one's position; drop the rest.
			if i == firstTextIdx && emitText {
				outs = append(outs, newText...)
				textGroupEmitted = true
				ops = append(ops, "native text chunk rewrite")
			}
			continue
		case i == d.id3Idx:
			if emitID3 {
				outs = append(outs, id3Out(newID3))
				id3Rewritten = true
				ops = append(ops, "ID3 chunk rewrite")
			}
			continue // ID3 present but now empty: drop it
		case ch.dupTag:
			// Redundant duplicate ID3 chunk: drop on rewrite so the output carries a
			// single, consistent copy rather than a stale shadow.
			ops = append(ops, "duplicate ID3 chunk drop")
			continue
		default:
			// A stale ID3-identified chunk reaches the default only when it parsed as
			// neither the authoritative ID3 (handled above) nor a marked duplicate -
			// i.e. a lone chunk whose body failed to decode as ID3, so no authoritative
			// ID3 was found. Drop it when we are writing a fresh ID3 chunk, so the output
			// never carries two ID3 chunks (which a re-parse would flag as a duplicate,
			// disagreeing with the returned document).
			if emitID3 && isID3Chunk(ch.id4()) {
				ops = append(ops, "stale ID3 chunk drop")
				continue
			}
			role := roleOther
			switch i {
			case d.ssndIdx:
				role = roleSSND
			case d.commIdx:
				role = roleCOMM
			}
			outs = append(outs, outChunk{id: ch.id, role: role, srcOff: ch.bodyOff, bodyLen: ch.bodyLen})
		}
	}

	// Insert newly created containers (native text then ID3) just before the SSND
	// chunk, the conventional position. The native group is created here only when
	// there were no native chunks to regroup in place.
	var created []outChunk
	if emitText && !textGroupEmitted {
		created = append(created, newText...)
		ops = append(ops, "native text chunk creation")
	}
	if emitID3 && !id3Rewritten {
		created = append(created, id3Out(newID3))
		ops = append(ops, "ID3 chunk creation")
	}
	if len(created) > 0 {
		outs = insertBeforeSSND(outs, created)
	}
	if emitID3 {
		if n := id3.APICCount(newID3); n > 0 {
			ops = append(ops, fmt.Sprintf("pictures: %d", n))
		}
	}
	return outs, ops
}

// id3Out builds the "ID3 " output chunk from a rendered ID3v2 tag.
func id3Out(t *id3.Tag) outChunk {
	body := id3.Render(t.WriteVersion(), t.Frames(), 0)
	return outChunk{id: [4]byte{'I', 'D', '3', ' '}, role: roleID3, body: body, bodyLen: int64(len(body))}
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

// chunkRole tags an output chunk so the result document can re-find the
// containers and the sound chunk without guessing from identifiers.
type chunkRole uint8

const (
	roleOther chunkRole = iota
	roleText
	roleID3
	roleCOMM
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
}

// outLayout is the byte-level result of assembling the output chunks: the new
// chunk list (with output offsets), the container/sound indices for the result
// document, the sound extent, and the total output size.
type outLayout struct {
	chunks   []chunk
	textIdx  []int
	commIdx  int
	ssndIdx  int
	id3Idx   int
	audioOff int64
	audioEnd int64
	total    int64
}

// assemble turns the output chunks into a rewrite segment list and recomputes the
// FORM size, returning the layout needed to build the post-write document. All
// sizes are big-endian, per IFF.
func assemble(d *doc, outs []outChunk) (segs []bits.Segment, lay outLayout, err error) {
	lay = outLayout{commIdx: -1, ssndIdx: -1, id3Idx: -1}
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
		switch oc.role {
		case roleText:
			lay.textIdx = append(lay.textIdx, idx)
		case roleID3:
			lay.id3Idx = idx
		case roleCOMM:
			lay.commIdx = idx
		case roleSSND:
			lay.ssndIdx = idx
			lay.audioOff = soundDataStart(running, oc.bodyLen)
			lay.audioEnd = running + oc.bodyLen
		}
		if oc.body != nil {
			segs = append(segs, bits.Lit(oc.body))
		} else {
			segs = append(segs, bits.Copy(oc.srcOff, oc.bodyLen))
		}
		running += oc.bodyLen
		if oc.bodyLen&1 == 1 {
			// Word-alignment pad. Always a literal zero: IFF defines pad bytes as zero
			// and not part of the data, and a malformed source may omit the final
			// chunk's pad entirely (so copying it would read past EOF).
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

// buildResult constructs the post-write Media so the engine can return a Document
// without re-parsing. Its canonical view is re-projected (via the same project
// used by Parse) from the containers actually written, so it equals a fresh parse
// of the output.
func buildResult(edited *core.Media, base *doc, newText []outChunk, newID3 *id3.Tag, lay outLayout) *core.Media {
	nd := &doc{
		chunks:   lay.chunks,
		formType: base.formType,
		commIdx:  lay.commIdx,
		ssndIdx:  lay.ssndIdx,
		id3Idx:   lay.id3Idx,
		textIdx:  lay.textIdx,
		audioOff: lay.audioOff,
		audioEnd: lay.audioEnd,
		comm:     base.comm,
		track:    base.track,
		size:     lay.total,
	}
	// Rebuild the decoded native text items from the written chunks so a re-edit of
	// the returned document (without re-parsing) sees the same values. newText is the
	// roleText chunk set that assemble recorded into lay.textIdx, in the same order.
	for _, oc := range newText {
		nd.texts = append(nd.texts, textItem{id: oc.id, raw: oc.body})
	}
	if lay.id3Idx >= 0 {
		nd.id3 = newID3
	}
	// The in-FORM trailing and out-of-FORM regions were appended verbatim at the
	// end of the output, in that order; record their new offsets so re-editing the
	// returned document (without re-parsing) still preserves them.
	nd.outerLen = base.outerLen
	nd.outerOff = lay.total - base.outerLen
	nd.trailingLen = base.trailingLen
	nd.trailingOff = nd.outerOff - base.trailingLen

	tags, pics, families, numericGenre := project(nd)
	return &core.Media{
		Format:     core.FormatAIFF,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Families:   families,
		// Recompute warnings from the written containers so the returned document
		// matches a fresh parse of the output: a dropped duplicate no longer warns, a
		// resolved numeric genre no longer warns, and a preserved encoder stamp still
		// does. (Duplicate-tag-block warnings are structural to the source and gone
		// once consolidated, so they are correctly absent here.)
		Warnings:   mediaWarnings(nd, numericGenre),
		Native:     nd,
		Identity:   core.Identity{Size: lay.total},
		AudioStart: lay.audioOff,
		AudioEnd:   lay.audioEnd,
	}
}
