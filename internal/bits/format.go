package bits

import "fmt"

// HumanBytes formats a byte count with a binary-magnitude unit (B, KiB, MiB,
// ...). It is the single definition shared by the CLI's display layer and the
// codecs' size-limit error messages, so a humanized "57.2 MiB" reads the same
// everywhere a byte count is shown to a person. Sub-1024 counts stay exact ("57
// B"); larger counts round to one decimal place.
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
	// The value is always < 1024 here, but one just under a unit boundary (e.g.
	// 1 MiB - 1 byte = 1023.999 KiB) rounds via %.1f to "1024.0"; promote it to the
	// next unit so it reads "1.0 MiB" rather than "1024.0 KiB". exp can never reach
	// the last prefix for an int64 (~8 EiB max), so the bound is defensive.
	if val >= 1023.95 && exp < len("KMGTPE")-1 {
		val /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", val, "KMGTPE"[exp])
}
