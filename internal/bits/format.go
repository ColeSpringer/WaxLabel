package bits

import "fmt"

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
