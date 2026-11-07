package aac

import (
	"context"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
)

// ADTS has no SBR field: HE-AAC declares the core coder and carries the
// extension in-frame. This file walks raw data blocks so ADTS geometry matches
// a player (and the same audio in MP4).

// sbrProbeFrames: SBR is in every HE-AAC frame, so a majority of the frames parsed
// among the first 16 decides; unsupported frames abstain but still count toward
// the 16, so a stream of them costs no more. Keeps recursive dumps cheap.
const sbrProbeFrames = 16

// probeWindow holds several real frames and at least one at the 8191-byte ADTS
// length ceiling, so a frame cannot straddle a refill forever.
const probeWindow = 16 << 10

// detectSBR reports whether the first sbrProbeFrames frames carry SBR, and whether
// a mono core's SBR payload carries parametric stereo. Unclaimed frames abstain;
// too few parsed frames report neither. Windows are read as [bits.FillWithin]
// reads: one past the end of the file ends the probe, and one that fails is an
// error. A window over limit is refused.
func detectSBR(ctx context.Context, src core.ReaderAtSized, start, end int64, h adtsHeader, limit int64) (sbr, ps bool, err error) {
	// SBR plays at 2x core rate; if 2x has no coded index, skip the probe.
	sbrRateIdx, ok := doubledRateIndex(h.sfIndex)
	if !ok {
		return false, false, nil
	}
	bufSize := min(int64(probeWindow), end-start)
	if err := bits.CheckAlloc(bufSize, limit); err != nil {
		return false, false, err
	}
	if bufSize <= int64(adtsHeaderSize) {
		return false, false, nil
	}
	buf := make([]byte, bufSize)
	var state mpeg4audio.SBRState
	examined, parsed, sbrFrames, sceFrames, psFrames := 0, 0, 0, 0, 0
	for off := start; off+int64(adtsHeaderSize) <= end && examined < sbrProbeFrames; {
		if e := ctx.Err(); e != nil {
			return false, false, e
		}
		n := min(end-off, int64(len(buf)))
		ok, err := bits.FillWithin(src, buf[:n], off)
		if err != nil {
			return false, false, err
		}
		if !ok {
			break
		}
		window := buf[:n]
		p := 0
		for p+adtsHeaderSize <= len(window) && examined < sbrProbeFrames {
			fh, ok := decodeADTS(window[p:])
			if !ok || p+fh.frameLength > len(window) {
				break // corrupt or straddling: refill from off+p
			}
			probeFrame(window[p:p+fh.frameLength], fh, sbrRateIdx, &state, &parsed, &sbrFrames, &sceFrames, &psFrames)
			examined++
			p += fh.frameLength
		}
		if p == 0 {
			break // nothing fit: do not spin
		}
		off += int64(p)
	}
	sbr = parsed > 0 && 2*sbrFrames > parsed
	ps = sbr && sceFrames > 0 && 2*psFrames > sceFrames
	return sbr, ps, nil
}

// probeFrame parses one frame's raw data block into the running votes. Unclaimed
// or unparseable frames abstain (do not count against).
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
	// Unparseable SBR payload abstains from the PS vote (same as SBR abstention).
	if got, ok := mpeg4audio.ParseSBRSingleChannel(info.SBRPayload, info.SBRBits, info.SBRCRC, sbrRateIdx, state); ok {
		*sceFrames++
		if got {
			*psFrames++
		}
	}
}

// doubledRateIndex is the sampling-frequency index of 2x rate i (SBR play rate).
// ok is false when 2x has no index.
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
