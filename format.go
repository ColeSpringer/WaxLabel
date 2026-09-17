package waxlabel

import (
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// HumanBytes formats a byte count with a binary unit (B, KiB, MiB, ...).
// Sub-1024 stays exact; larger values round to one decimal and promote at unit
// boundaries (1 MiB - 1 reads "1.0 MiB", not "1024.0 KiB").
func HumanBytes(n int64) string { return bits.HumanBytes(n) }

// FormatChapterTime renders a chapter offset as H:MM:SS.mmm.
func FormatChapterTime(d time.Duration) string { return core.FormatChapterTime(d) }
