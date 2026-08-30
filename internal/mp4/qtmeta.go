package mp4

import (
	"encoding/binary"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// This file decodes the two QuickTime metadata stores that sit beside the iTunes
// moov.udta.meta.ilst an MP4 normally carries:
//
//   - An mdta-handler meta box, whose "keys" atom is an index of full key names and whose
//     ilst items are keyed by that index rather than by a four-character atom name. ffmpeg
//     writes this shape under "-movflags +use_metadata_tags".
//   - Classic QuickTime text atoms ("\xa9nam", "\xa9swr", ...) sitting directly under
//     moov.udta with no meta wrapper at all, which is what a plain .mov carries.
//
// A file can carry all three stores at once. Every store contributes, so nothing a file
// holds is invisible, and each contributes under its own Contribution.Source label
// ("\xa9nam" for the iTunes ilst, "mdta:title", "udta.\xa9nam"). BuildFamilies marks a key
// unselected when two sources disagree, so a disagreement between two stores lands in
// lint's conflicting-families rule instead of one store silently winning.

// mdtaHandler is the meta hdlr handler_type for a keys-indexed metadata store; mdirHandler
// is the iTunes one. A meta with any other handler is decoded as iTunes, which is what the
// four-character dispatch already assumed.
const (
	mdtaHandler = "mdta"
	mdirHandler = "mdir"
)

// maxKeysEntries caps the keys index and maxQTTextEntries the per-atom language list, so a
// crafted count or a long run of 4-byte entries cannot drive an unbounded allocation. Both
// sit far above any plausible file.
const (
	maxKeysEntries   = 4096
	maxQTTextEntries = 4096
	// qtTextMax is the largest text one entry can store: its length is a 16-bit field.
	qtTextMax = 0xFFFF
)

// parseKeys decodes a "keys" box payload into the key-name index. Entry i of the returned
// slice is ilst index i+1 (the ilst item names are 1-based). Each entry is
// [uint32 key_size][4cc key_namespace][key_value], with key_size covering the whole entry.
// A malformed box yields no names, so every ilst item falls back to being preserved
// verbatim rather than resolving against a half-read index.
func parseKeys(p []byte) []string {
	if len(p) < 8 {
		return nil
	}
	count := int64(binary.BigEndian.Uint32(p[4:8]))
	if count <= 0 || count > maxKeysEntries {
		return nil
	}
	n := int64(len(p))
	pos := int64(8)
	out := make([]string, 0, count)
	for range count {
		if pos+8 > n {
			return nil
		}
		size := int64(binary.BigEndian.Uint32(p[pos : pos+4]))
		if size < 8 || pos+size > n {
			return nil
		}
		// The namespace ("mdta" in every file seen in the wild) is not part of the name. A name
		// that is not valid UTF-8 would reach the canonical key vocabulary and the native view,
		// which every sibling MP4 decoder refuses, so the whole table is rejected.
		name := p[pos+8 : pos+size]
		if !utf8.Valid(name) {
			return nil
		}
		out = append(out, string(name))
		pos += size
	}
	return out
}

// renderKeys builds a "keys" box payload from a key-name index, the inverse of parseKeys.
// Every entry is written under the "mdta" namespace, which is the only one an ilst index
// refers to.
func renderKeys(names []string) []byte {
	out := make([]byte, 8, 8+len(names)*24)
	binary.BigEndian.PutUint32(out[4:8], uint32(len(names)))
	for _, name := range names {
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(name)))
		copy(hdr[4:8], mdtaHandler)
		out = append(out, hdr[:]...)
		out = append(out, name...)
	}
	return out
}

// mdtaIndex returns the 1-based keys index an mdta ilst item's four-byte name encodes, or 0
// when the name is not a usable index. An item naming an index past the keys table is left
// unresolved (and so preserved verbatim) rather than dropped.
func mdtaIndex(name [4]byte) uint32 { return binary.BigEndian.Uint32(name[:]) }

// resolveMdtaKey returns the key name an mdta ilst item resolves to through the keys index,
// or "" when it resolves to nothing.
//
// The bound is compared in uint64: a crafted four-cc reads as an index above 2^31, which
// int(i) turns negative on a 32-bit build, so an "int(i) > len(keys)" guard would pass and
// the index panic.
func resolveMdtaKey(name [4]byte, keys []string) string {
	i := mdtaIndex(name)
	if i == 0 || uint64(i) > uint64(len(keys)) {
		return ""
	}
	return keys[i-1]
}

// qtTextEntry is one [uint16 size][uint16 language]<text> entry of a classic QuickTime udta
// text atom. Several entries can sit back to back in one atom, one per language, which is
// how ffprobe comes to report both "title" and "title-eng" for a single "\xa9nam".
type qtTextEntry struct {
	lang uint16
	text string
}

// QuickTime language codes that mean "no particular language": 0 is the Macintosh English
// code (and the value a writer that does not care emits), langUnd is the packed ISO-639-2
// "und", and langEng is packed "eng".
const (
	langMacEnglish uint16 = 0
	langUnd        uint16 = 0x55C4
	langEng        uint16 = 0x15C7
)

// udtaText is one decoded QuickTime text atom sitting directly under moov.udta: its
// four-character name, its parsed entries, and the canonical key it maps to. A file can
// hold several entries for one atom; entries beyond the canonical one are preserved
// verbatim by the writer and never dropped.
type udtaText struct {
	ref     atomRef
	name    [4]byte
	key     tag.Key
	entries []qtTextEntry
}

func (u udtaText) id() string { return string(u.name[:]) }

// parseQTText decodes a udta text atom payload as a back-to-back sequence of
// [uint16 size][uint16 language]<text> entries, where size counts the text bytes only. It
// returns ok == false when the payload does not tile exactly, so the caller can fall back to
// the ilst-style "data" shape some writers use for the same atom names.
func parseQTText(p []byte) ([]qtTextEntry, bool) {
	n := int64(len(p))
	if n < 4 {
		return nil, false
	}
	var out []qtTextEntry
	pos := int64(0)
	for pos+4 <= n {
		size := int64(binary.BigEndian.Uint16(p[pos : pos+2]))
		lang := binary.BigEndian.Uint16(p[pos+2 : pos+4])
		if pos+4+size > n || len(out) >= maxQTTextEntries {
			return nil, false
		}
		text := p[pos+4 : pos+4+size]
		if !utf8.Valid(text) {
			return nil, false // preserved verbatim instead, as an undecodable ilst item is
		}
		out = append(out, qtTextEntry{lang: lang, text: string(text)})
		pos += 4 + size
	}
	if pos != n || len(out) == 0 {
		return nil, false
	}
	return out, true
}

// renderQTText is the inverse of parseQTText. A text longer than the 16-bit size field is
// truncated on a rune boundary rather than wrapping the length; udtaCanHoldAll keeps a
// canonical value that long out of this store, so only a preserved entry can reach it.
func renderQTText(entries []qtTextEntry) []byte {
	var out []byte
	for _, e := range entries {
		text := truncateUTF8(e.text, qtTextMax)
		var hdr [4]byte
		binary.BigEndian.PutUint16(hdr[0:2], uint16(len(text)))
		binary.BigEndian.PutUint16(hdr[2:4], e.lang)
		out = append(out, hdr[:]...)
		out = append(out, text...)
	}
	return out
}

// canonicalEntry returns the index of the entry that supplies the canonical value: the first
// whose language is undefined or English, else the first entry. Every other entry is
// preserved verbatim, so a multi-language atom keeps its other translations.
func canonicalEntry(entries []qtTextEntry) int {
	for i, e := range entries {
		if e.lang == langMacEnglish || e.lang == langUnd || e.lang == langEng {
			return i
		}
	}
	return 0
}

// decodeUdtaText decodes one direct udta child into a udtaText, or reports false when the
// atom is not a mapped QuickTime text atom or its payload parses as neither shape. Both
// shapes are accepted because some writers put an ilst-style "data" box under a udta-level
// atom instead of the classic entry sequence.
func decodeUdtaText(ref atomRef, payload []byte) (udtaText, bool) {
	name := ref.name
	key, ok := mapping.MP4UdtaTextKey(string(name[:]))
	if !ok {
		return udtaText{}, false
	}
	if entries, ok := parseQTText(payload); ok {
		return udtaText{ref: ref, name: name, key: key, entries: entries}, true
	}
	// The ilst "data" shape: one or more [size]["data"][type][locale]<value> boxes. Reuse the
	// ilst decoder so the two spellings of the same atom project identically.
	atoms, ok := parseDataAtoms(payload)
	if !ok {
		return udtaText{}, false
	}
	var entries []qtTextEntry
	for _, a := range atoms {
		if a.typ != typeUTF8 && a.typ != typeImplicit || !utf8.Valid(a.value) {
			return udtaText{}, false
		}
		entries = append(entries, qtTextEntry{lang: langUnd, text: string(a.value)})
	}
	return udtaText{ref: ref, name: name, key: key, entries: entries}, true
}

// udtaContributions projects the decoded udta-level text atoms into canonical
// contributions. Each carries a "udta."-prefixed source label so a value that disagrees with
// the same key in an ilst surfaces as a conflicting family rather than one store winning
// silently.
func udtaContributions(texts []udtaText) []core.Contribution {
	var out []core.Contribution
	for _, u := range texts {
		if len(u.entries) == 0 {
			continue
		}
		v := u.entries[canonicalEntry(u.entries)].text
		if v == "" {
			continue
		}
		out = append(out, core.Contribution{Key: u.key, Value: v, Source: "udta." + u.id()})
	}
	return out
}
