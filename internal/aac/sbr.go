package aac

import (
	"context"
	"io"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
)

// An ADTS header has no field for SBR: a raw HE-AAC stream declares the core coder and
// carries the extension inside its frames, where only a syntax walk finds it. That is what
// this file does, so an ADTS stream reports the geometry a player produces rather than half
// of it, and agrees with the same audio muxed into MP4.

// sbrProbeFrames bounds the frame walk: SBR data is present in every frame of an HE-AAC
// stream, so a majority over the first sixteen frames decides, and a parse failure (a
// corrupt or unsupported frame) simply abstains. Sixteen keeps a recursive dump over a
// library cheap; the probe runs on every ADTS parse.
const sbrProbeFrames = 16

// probeWindow is the working buffer the probe reads through. It holds several frames of any
// real stream and, at the 8191-byte ceiling an ADTS length field can declare, always at
// least one, so no frame can straddle a refill forever.
const probeWindow = 16 << 10

// detectSBR walks the first frames of an ADTS stream and reports whether their raw data
// blocks carry SBR, and whether a mono core's SBR payload carries the parametric stereo
// extension. A read failure is returned; a frame this parser does not claim abstains, and a
// stream where too few frames parse reports neither.
func detectSBR(ctx context.Context, src core.ReaderAtSized, start, end int64, h adtsHeader, limit int64) (sbr, ps bool, err error) {
	// SBR plays at twice the core rate, so a core rate whose double is not a coded rate
	// cannot be an SBR stream's core; there is nothing to probe.
	sbrRateIdx, ok := doubledRateIndex(h.sfIndex)
	if !ok {
		return false, false, nil
	}
	// The window is a working buffer, not the whole probe: sixteen frames of a real HE-AAC
	// stream are a couple of kilobytes, so reading the 128 KiB the frame-length ceiling
	// allows for would read (and allocate) fifty times what the walk consumes. Refill only
	// when a frame straddles the end, exactly as the duration walk does.
	//
	// The probe is also an enrichment, not a requirement: a caller with a small
	// MaxAllocBytes must still parse the file, so the window shrinks to the limit rather
	// than failing the read, and one too small for a frame simply abstains.
	bufSize := min(int64(probeWindow), end-start, limit)
	if bufSize <= int64(adtsHeaderSize) {
		return false, false, nil
	}
	buf := make([]byte, bufSize)
	var state mpeg4audio.SBRState
	parsed, sbrFrames, sceFrames, psFrames := 0, 0, 0, 0
	for off := start; off+int64(adtsHeaderSize) <= end && parsed < sbrProbeFrames; {
		if e := ctx.Err(); e != nil {
			return false, false, e
		}
		n := min(end-off, int64(len(buf)))
		got, rerr := src.ReadAt(buf[:n], off)
		// A short read means the source no longer holds the bytes the audio region claimed;
		// a non-EOF error is a genuine fault. Either way the probe stops with what it has
		// rather than failing a parse that has already read its geometry.
		if rerr != nil && rerr != io.EOF {
			return false, false, nil
		}
		window := buf[:got]
		p := 0
		for p+adtsHeaderSize <= len(window) && parsed < sbrProbeFrames {
			fh, ok := decodeADTS(window[p:])
			if !ok || p+fh.frameLength > len(window) {
				break // corrupt bytes, or a frame straddling the window: refill from off+p
			}
			probeFrame(window[p:p+fh.frameLength], fh, sbrRateIdx, &state, &parsed, &sbrFrames, &sceFrames, &psFrames)
			p += fh.frameLength
		}
		if p == 0 {
			break // no whole frame fit the window: do not spin
		}
		off += int64(p)
		if rerr != nil {
			break // benign EOF after the whole frames already read
		}
	}
	sbr = parsed > 0 && 2*sbrFrames > parsed
	ps = sbr && sceFrames > 0 && 2*psFrames > sceFrames
	return sbr, ps, nil
}

// probeFrame parses one whole ADTS frame's raw data block and folds its answer into the
// running counts. A frame this parser does not claim, or one whose block does not parse,
// abstains from both votes rather than counting against them.
func probeFrame(frame []byte, h adtsHeader, sbrRateIdx int, state *mpeg4audio.SBRState, parsed, sbrFrames, sceFrames, psFrames *int) {
	if h.objectType != 2 || h.rawBlocks != 0 || h.headerLen() >= len(frame) {
		return
	}
	info, err := mpeg4audio.ParseRawDataBlock(frame[h.headerLen():], mpeg4audio.FrameConfig{
		ObjectType:    h.objectType,
		SampleRateIdx: h.sfIndex,
		ChannelConfig: h.chanConfig,
	})
	if err != nil {
		return
	}
	*parsed++
	if info.SBR {
		*sbrFrames++
	}
	if info.SBRPayload == nil {
		return
	}
	// A payload that does not parse abstains from the parametric-stereo vote entirely, the
	// same way an unparseable frame abstains from the SBR one: counting it as a channel
	// element seen would turn a failure into a vote against.
	if got, ok := mpeg4audio.ParseSBRSingleChannel(info.SBRPayload, info.SBRBits, info.SBRCRC, sbrRateIdx, state); ok {
		*sceFrames++
		if got {
			*psFrames++
		}
	}
}

// doubledRateIndex returns the sampling-frequency index of twice the rate at index i, the
// rate an SBR stream plays at. ok is false when the doubled rate has no index of its own.
func doubledRateIndex(i int) (int, bool) {
	want := mpeg4audio.SampleRate(i) * 2
	if want == 0 {
		return 0, false
	}
	for j := range 13 {
		if mpeg4audio.SampleRate(j) == want {
			return j, true
		}
	}
	return 0, false
}
