package waxlabel

import (
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// HumanBytes formats a byte count with a binary-magnitude unit (B, KiB, MiB, ...)
// - the same formatting WaxLabel uses in its own text output and size-limit error
// messages, exposed so a consumer can render sizes consistently with it. Sub-1024
// counts stay exact ("57 B"); larger counts round to one decimal place and promote
// at a unit boundary, so 1 MiB - 1 byte reads "1.0 MiB", not "1024.0 KiB".
func HumanBytes(n int64) string { return bits.HumanBytes(n) }

// FormatChapterTime renders a chapter offset as H:MM:SS.mmm (millisecond
// precision), the same format WaxLabel's chapter listing uses, exposed so a
// consumer renders chapter timestamps consistently with it. It is also what the
// chapter sanity warnings use, so a warning's timestamp matches the listing.
func FormatChapterTime(d time.Duration) string { return core.FormatChapterTime(d) }
