package id3

import "encoding/binary"

// knownFrameIDs are the v2.3 and v2.4 frame IDs the specifications define, plus the ones iTunes and
// its peers write. The dry walks of [v24FrameSizesArePlain] count them: a walk that reads the wrong
// sizes lands inside frame bodies, where four uppercase letters of text are common but a known ID
// rarely is.
var knownFrameIDs = func() map[string]bool {
	m := map[string]bool{}
	for _, id := range []string{
		"AENC", "APIC", "ASPI", "COMM", "COMR", "ENCR", "EQU2", "EQUA", "ETCO", "GEOB", "GRID",
		"IPLS", "LINK", "MCDI", "MLLT", "OWNE", "PCNT", "POPM", "POSS", "PRIV", "RBUF", "RVA2",
		"RVAD", "RVRB", "SEEK", "SIGN", "SYLT", "SYTC", "TALB", "TBPM", "TCOM", "TCON", "TCOP",
		"TDAT", "TDEN", "TDLY", "TDOR", "TDRC", "TDRL", "TDTG", "TENC", "TEXT", "TFLT", "TIME",
		"TIPL", "TIT1", "TIT2", "TIT3", "TKEY", "TLAN", "TLEN", "TMCL", "TMED", "TMOO", "TOAL",
		"TOFN", "TOLY", "TOPE", "TORY", "TOWN", "TPE1", "TPE2", "TPE3", "TPE4", "TPOS", "TPRO",
		"TPUB", "TRCK", "TRDA", "TRSN", "TRSO", "TSIZ", "TSOA", "TSOP", "TSOT", "TSRC", "TSSE",
		"TSST", "TXXX", "TYER", "UFID", "USER", "USLT", "WCOM", "WCOP", "WOAF", "WOAR", "WOAS",
		"WORS", "WPAY", "WPUB", "WXXX",
		// iTunes and later conventions.
		"CHAP", "CTOC", "GRP1", "MVIN", "MVNM", "PCST", "TCAT", "TCMP", "TDES", "TGID", "TKWD",
		"TSO2", "TSOC", "WFED",
	} {
		m[id] = true
	}
	return m
}()

// v24FrameSizesArePlain reports whether an ID3v2.4 frame region's sizes are plain 32-bit integers
// rather than the sync-safe ones the specification requires, as iTunes once wrote them. A size under
// 128 reads the same either way, so only a larger frame can tell; a region where no size reads
// differently is sync-safe. The headers are walked both ways, counting the known frame IDs each walk
// meets and how far past the region it ends: plain wins with more known IDs, or as many while the
// sync-safe walk runs past the end and the plain one does not (mutagen's rule). A tie otherwise reads
// sync-safe, as mutagen, ffmpeg and TagLib read it; where that is wrong, the frame walk stops inside a
// frame and reports the rest unreadable ([parseFrames]). Each walk stops after maxElements headers.
func v24FrameSizesArePlain(body []byte, maxElements int) bool {
	plainKnown, plainPast, differs := countWalk(body, true, maxElements)
	if !differs {
		return false
	}
	safeKnown, safePast, _ := countWalk(body, false, maxElements)
	return plainKnown > safeKnown || plainKnown == safeKnown && safePast >= 1 && plainPast <= 1
}

// frameSize reads the size field of the v2.4 frame header h, plain or sync-safe.
func frameSize(h []byte, plain bool) int64 {
	if plain {
		return int64(binary.BigEndian.Uint32(h[4:8]))
	}
	return syncSafe(h[4:8])
}

// countWalk walks the frame headers of body with plain or sync-safe sizes as mutagen does: it goes on
// by each size whatever the ID, counts the known IDs, and stops at ten zero bytes or the end. past is
// how far past the end of body the walk ended, at most 0 when it stopped at zeros. differs reports a
// size met that reads differently the other way.
func countWalk(body []byte, plain bool, maxElements int) (known int, past int64, differs bool) {
	n := int64(len(body))
	o := int64(0)
	for steps := 0; o < n-10; steps++ {
		if maxElements > 0 && steps >= maxElements {
			break
		}
		h := body[o : o+10]
		if allZero(h) {
			return known, -((n - o) % 10), differs
		}
		size := frameSize(h, plain)
		differs = differs || size != frameSize(h, !plain)
		o += 10 + size
		if knownFrameIDs[string(h[:4])] {
			known++
		}
	}
	return known, o - n, differs
}

// allZero reports whether every byte of b is zero.
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
