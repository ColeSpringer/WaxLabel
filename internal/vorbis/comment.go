// Package vorbis implements the byte-level Vorbis comment list codec, the
// FLAC-style PICTURE block codec, and the canonical projection / minimal-change
// rebuild shared by every format that stores tags as Vorbis comments - FLAC and
// Ogg Vorbis/Opus. It is internal through v0.x and reimplemented from the
// Vorbis-comment and FLAC picture specifications; reference implementations were
// consulted for design only.
//
// A comment list is the format-neutral core: a vendor string and "NAME=value"
// entries with little-endian length prefixes. FLAC wraps it in a metadata
// block; Ogg Vorbis prefixes a "\x03vorbis" signature and appends a framing
// bit; Ogg Opus prefixes "OpusTags" and may append padding. Those wrappers live
// in the respective codecs; the list codec here is shared.
package vorbis

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// Comment is one Vorbis "NAME=value" entry. The original name spelling is kept
// so unedited comments preserve their exact form on rewrite.
type Comment struct {
	Name  string
	Value string
}

// ParseCommentList decodes a comment list (little-endian lengths): a vendor
// string, a count, then that many "NAME=value" entries. It returns the number
// of body bytes consumed so a caller can handle whatever follows the list - the
// Vorbis framing bit, or Opus comment-header padding. Entries without '=' are
// dropped from the result (but still consumed).
func ParseCommentList(body []byte, limit int64) (vendor string, comments []Comment, n int64, err error) {
	c := bits.NewCursor(bytes.NewReader(body), int64(len(body)), limit)
	vlen := int64(c.U32LE())
	vendor = string(c.Bytes(vlen))
	count := c.U32LE()
	for i := uint32(0); i < count; i++ {
		if c.Err() != nil {
			break
		}
		l := int64(c.U32LE())
		entry := c.Bytes(l)
		if c.Err() != nil {
			break
		}
		name, value, ok := strings.Cut(string(entry), "=")
		if !ok {
			continue // malformed entry without '='; drop from projection
		}
		comments = append(comments, Comment{Name: name, Value: value})
	}
	if c.Err() != nil {
		return vendor, comments, c.Pos(), fmt.Errorf("vorbis comment: %w", c.Err())
	}
	return vendor, comments, c.Pos(), nil
}

// RenderCommentList encodes a vendor string and comments into a list body
// (little-endian lengths, no signature or framing). Deterministic: same inputs
// produce identical bytes.
func RenderCommentList(vendor string, comments []Comment) []byte {
	var buf bytes.Buffer
	writeU32LE(&buf, uint32(len(vendor)))
	buf.WriteString(vendor)
	writeU32LE(&buf, uint32(len(comments)))
	for _, cm := range comments {
		entry := cm.Name + "=" + cm.Value
		writeU32LE(&buf, uint32(len(entry)))
		buf.WriteString(entry)
	}
	return buf.Bytes()
}

// Project builds the canonical TagSet and the family/source view from a comment
// list, preserving order. A canonical key fed by two or more distinct native
// field names with disagreeing values (e.g. DATE=2020 and YEAR=2019, both
// mapping to RecordingDate) is a genuine conflict and is marked unselected so it
// surfaces in the family view and Lint. Repeats of the same native name
// (ARTIST=A, ARTIST=B) are an ordinary multi-value, not a conflict.
func Project(comments []Comment) (tag.TagSet, []core.FamilyValue) {
	ts := tag.NewTagSet()
	famIndex := map[tag.Key]int{}
	names := map[tag.Key]map[string]bool{} // distinct native names per key
	var fams []core.FamilyValue
	for _, cm := range comments {
		key := mapping.CanonicalVorbis(cm.Name)
		ts.Add(key, cm.Value)
		if i, ok := famIndex[key]; ok {
			fams[i].Values = append(fams[i].Values, cm.Value)
		} else {
			famIndex[key] = len(fams)
			names[key] = map[string]bool{}
			fams = append(fams, core.FamilyValue{
				Key: key, Family: core.FamilyVorbis, Scope: core.ScopeTrack,
				Values: []string{cm.Value}, Selected: true,
			})
		}
		names[key][strings.ToUpper(cm.Name)] = true
	}
	for key, i := range famIndex {
		if len(names[key]) > 1 && distinctValues(fams[i].Values) > 1 {
			fams[i].Selected = false
		}
	}
	return ts, fams
}

// Rebuild produces the new comment list with minimal change: unchanged comments
// keep their exact spelling and position; a changed key's new values replace its
// first original occurrence (later duplicates and aliases of that key are
// dropped, deduping inherited noise); newly added keys are appended in edited
// order.
func Rebuild(orig []Comment, edited tag.TagSet, changed map[tag.Key]bool) []Comment {
	emitted := map[tag.Key]bool{}
	out := make([]Comment, 0, len(orig))
	emit := func(k tag.Key) {
		vals, _ := edited.Get(k)
		name := mapping.VorbisName(k)
		for _, v := range vals {
			out = append(out, Comment{Name: name, Value: v})
		}
		emitted[k] = true
	}
	for _, cm := range orig {
		k := mapping.CanonicalVorbis(cm.Name)
		if changed[k] {
			if !emitted[k] {
				emit(k) // replace in place; nothing emitted if the key was cleared
			}
			continue
		}
		out = append(out, cm)
	}
	for _, k := range edited.Keys() {
		if changed[k] && !emitted[k] {
			emit(k)
		}
	}
	return out
}

// DiffKeys returns the canonical keys whose values differ between base and
// edited (added, removed, or modified).
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	changed := map[tag.Key]bool{}
	for _, k := range base.Keys() {
		bv, _ := base.Get(k)
		ev, has := edited.Get(k)
		if !has || !slices.Equal(bv, ev) {
			changed[k] = true
		}
	}
	for _, k := range edited.Keys() {
		if !base.Has(k) {
			changed[k] = true
		}
	}
	return changed
}

// EncoderNoise flags inherited transcoder stamps (e.g. ffmpeg's
// "encoder=Lavf..." comment or vendor string), the typical signature of a file
// acquired by transcoding.
func EncoderNoise(vendor string, comments []Comment) []core.Warning {
	var ws []core.Warning
	if core.IsTranscoderStamp(vendor) {
		ws = core.Warn(ws, core.WarnInheritedEncoder, "vendor string is a transcoder stamp: "+vendor)
	}
	for _, cm := range comments {
		if strings.EqualFold(cm.Name, "ENCODER") && core.IsTranscoderStamp(cm.Value) {
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder comment: "+cm.Value)
		}
	}
	return ws
}

// distinctValues counts the distinct case- and space-insensitive values.
func distinctValues(vals []string) int {
	seen := map[string]bool{}
	for _, v := range vals {
		seen[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return len(seen)
}

func writeU32LE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
