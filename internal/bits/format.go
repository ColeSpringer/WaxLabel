package bits

import (
	"fmt"
	"strconv"
	"strings"
)

// HumanBytes formats n with binary units (B, KiB, ...). Sub-1024 exact; else one decimal.
func HumanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	// Promote 1023.9 KiB-style values to the next unit instead of "1024.0 KiB".
	if val >= 1023.95 && exp < len("KMGTPE")-1 {
		val /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", val, "KMGTPE"[exp])
}

// SizeOverLimit formats a size and the limit it exceeds for a refusal message. Where the units
// would print the two alike ("16.0 MiB, max 16.0 MiB"), each carries its exact byte count.
func SizeOverLimit(n, limit int64) (got, max string) {
	got, max = HumanBytes(n), HumanBytes(limit)
	if got == max {
		got, max = fmt.Sprintf("%s (%d bytes)", got, n), fmt.Sprintf("%s (%d bytes)", max, limit)
	}
	return got, max
}

// GroupThousands formats n in decimal with a comma between each group of three digits.
func GroupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	digits := len(s)
	if n < 0 {
		digits--
	}
	var b strings.Builder
	for i, c := range s {
		if left := len(s) - i; i > len(s)-digits && left%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
