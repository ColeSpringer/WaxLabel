package core

import (
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/tag"
)

// LegacyPolicy controls legacy tag containers (leading ID3v2, trailing ID3v1, APEv2) on write.
type LegacyPolicy uint8

const (
	// LegacyPreserve keeps legacy containers and warns on conflict.
	LegacyPreserve LegacyPolicy = iota
	// LegacyStrip removes them; warns on data loss (WarnLegacyStripDropped).
	LegacyStrip
)

func (p LegacyPolicy) String() string {
	switch p {
	case LegacyStrip:
		return "strip"
	default:
		return "preserve"
	}
}

// PaddingPolicy controls post-metadata free space for in-place growth.
type PaddingPolicy struct {
	// Target is desired padding bytes after rewrite.
	Target int64
	// Min/Max bound written padding. Min is reuse floor for ReuseInPlace. Max==0: no cap.
	Min int64
	Max int64
	// ReuseInPlace reuses existing padding when content still fits and leftover >= Min.
	ReuseInPlace bool
}

// DefaultPadding is the FLAC-oriented default (8 KiB target, reuse in place).
var DefaultPadding = PaddingPolicy{Target: 8192, Min: 0, Max: 1 << 20, ReuseInPlace: true}

// ClampTarget returns Target clamped to Min/Max. Max==0 means no upper bound here.
func (p PaddingPolicy) ClampTarget() int64 {
	v := p.Target
	if p.Max > 0 && v > p.Max {
		v = p.Max
	}
	if v < p.Min {
		v = p.Min
	}
	if v < 0 {
		v = 0
	}
	return v
}

// ReuseOrTarget sizes padding for a front metadata region (MP3/AAC ID3).
// Reuses in place when content fits and leftover >= Min; else ClampTarget.
func (p PaddingPolicy) ReuseOrTarget(origLen, contentLen int64) int64 {
	if p.ReuseInPlace && origLen >= contentLen && origLen-contentLen >= p.Min {
		return origLen - contentLen
	}
	return p.ClampTarget()
}

// ID3MultiValuePolicy controls multi-value text in ID3v2.3. v2.4 always NUL-separates.
type ID3MultiValuePolicy uint8

const (
	// ID3MultiNullSep: one frame, NUL-separated (v2.4 style).
	ID3MultiNullSep ID3MultiValuePolicy = iota
	// ID3MultiRepeatFrame: one frame per value.
	ID3MultiRepeatFrame
	// ID3MultiSlash: join with " / " (not separable on read).
	ID3MultiSlash
)

func (p ID3MultiValuePolicy) String() string {
	switch p {
	case ID3MultiRepeatFrame:
		return "repeat-frame"
	case ID3MultiSlash:
		return "slash-join"
	default:
		return "null-separated"
	}
}

// DefaultMaxSourceBytes caps non-seekable stream buffering (stdin, OpenSource). <=0 disables.
const DefaultMaxSourceBytes int64 = 2 << 30 // 2 GiB

// ParseOptions are the resolved (non-functional) parse settings a codec sees.
type ParseOptions struct {
	Limits bits.Limits
	// SourceName is display-only for detection errors (e.g. stdin as "-").
	SourceName string
	// MaxSourceBytes caps stream buffering for OpenSource/stdin. >0 enforces; <=0 unbounded.
	MaxSourceBytes int64
}

// DefaultParseOptions returns parse options with conservative limits.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{Limits: bits.DefaultLimits, MaxSourceBytes: DefaultMaxSourceBytes}
}

// WriteOptions are the resolved write settings a codec sees.
type WriteOptions struct {
	Limits  bits.Limits
	Padding PaddingPolicy
	// PaddingExplicit: user requested padding; forces write even if tags unchanged.
	PaddingExplicit bool
	Legacy          LegacyPolicy
	PreserveModTime bool
	// VerifyEssence verifies audio hash on copy.
	VerifyEssence bool
	// NumericGenre writes recognized genre as numeric ID3 TCON reference.
	NumericGenre bool
	// ID3Multi selects ID3v2.3 multi-value encoding.
	ID3Multi ID3MultiValuePolicy
	// Touched: keys named by the edit (set/add/clear), including unchanged re-sets.
	Touched map[tag.Key]bool
	// AllowUnrecognizedPictures embeds pictures without recognized image headers.
	AllowUnrecognizedPictures bool
	// StripEncoderStamp removes transcoder stamps (WAV ISFT, FLAC/Ogg vendor). Gates on IsTranscoderStamp.
	StripEncoderStamp bool
	// WebMSubset: file-less Matroska capability query uses WebM restrictions.
	WebMSubset bool
	// Carried: transfer/copy write; suppresses author conveniences (e.g. SYLT language fallback).
	Carried bool
	// AllowUnsupportedDrop drops unstorable structural edits with warning; --strict fails.
	AllowUnsupportedDrop bool
	// KeepR128Gains leaves R128_* tags unrebased when output gain changes.
	KeepR128Gains bool
	// SyncedLyricsCleared: skip SYLT language/descriptor fallback after explicit clear.
	SyncedLyricsCleared bool
}

// DefaultWriteOptions returns the preservation-first defaults.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{
		Limits:  bits.DefaultLimits,
		Padding: DefaultPadding,
		Legacy:  LegacyPreserve,
	}
}
