package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// byteSizeValue is the pflag.Value for --max-size. It parses a human byte size
// and renders via HumanBytes so help and errors match other size output. 0 means
// unlimited. The library formats sizes but has no parser; this owns the reverse.
type byteSizeValue int64

func (v *byteSizeValue) Set(s string) error {
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	*v = byteSizeValue(n)
	return nil
}

func (v *byteSizeValue) String() string {
	if *v <= 0 {
		return "0"
	}
	return wl.HumanBytes(int64(*v))
}

func (v *byteSizeValue) Type() string { return "size" }

// parseByteSize converts a human byte-size string to a byte count. Accepts a bare
// number optionally followed by a unit: KB/MB/GB/TB are decimal (1000^n);
// KiB/MiB/GiB/TiB and bare K/M/G/T are binary (1024^n), case-insensitive, optional
// space before the unit. Bare letters are binary so values round-trip with
// HumanBytes. "0" means unlimited. Leading '+' allowed; negative/empty/unparseable
// rejected.
func parseByteSize(s string) (int64, error) {
	total, err := byteSizeFloat(s)
	if err != nil {
		return 0, err
	}
	// float64(MaxInt64) rounds up to 2^63; reject that too so int64(total) cannot
	// wrap to a negative "unlimited".
	if total >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return int64(total), nil
}

// byteSizeFloat is the numeric half of parseByteSize before int64 truncation, so
// callers that must reject a fractional result can see one.
func byteSizeFloat(s string) (float64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	// Leading '-' would otherwise fall through to "expected a leading number"
	// (the digit scan never consumes the sign). Strip redundant '+'.
	if strings.HasPrefix(trimmed, "-") {
		return 0, fmt.Errorf("invalid size %q: must not be negative", s)
	}
	trimmed = strings.TrimPrefix(trimmed, "+")
	// Split leading number (digits + optional '.') from unit.
	i := 0
	for i < len(trimmed) && (trimmed[i] >= '0' && trimmed[i] <= '9' || trimmed[i] == '.') {
		i++
	}
	numPart, unitPart := trimmed[:i], strings.TrimSpace(trimmed[i:])
	if numPart == "" {
		return 0, fmt.Errorf("invalid size %q: expected a leading number", s)
	}
	num, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %v", s, err)
	}
	if num < 0 {
		return 0, fmt.Errorf("invalid size %q: must not be negative", s)
	}
	mult, err := unitMultiplier(unitPart)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %v", s, err)
	}
	return num * float64(mult), nil
}

// parseByteSizeExact rejects sizes that would truncate to a whole byte
// (e.g. "0.4", "1.9KiB"). "1.5KiB" (1536) passes.
func parseByteSizeExact(s string) (int64, error) {
	n, err := parseByteSize(s)
	if err != nil {
		return 0, err
	}
	total, err := byteSizeFloat(s)
	if err != nil {
		return 0, err
	}
	if total != math.Trunc(total) {
		return 0, fmt.Errorf("invalid size %q: must be a whole number of bytes", s)
	}
	return n, nil
}

// unitMultiplier maps a size unit suffix to its byte multiplier. Empty/"B" = 1;
// K/M/G/T or *iB = binary (1024); KB/MB/... = decimal (1000). Listed explicitly
// so the accepted set is obvious. Binary matches HumanBytes magnitudes.
func unitMultiplier(u string) (int64, error) {
	switch strings.ToLower(u) {
	case "", "b":
		return 1, nil
	case "k", "kib":
		return 1 << 10, nil
	case "kb":
		return 1_000, nil
	case "m", "mib":
		return 1 << 20, nil
	case "mb":
		return 1_000_000, nil
	case "g", "gib":
		return 1 << 30, nil
	case "gb":
		return 1_000_000_000, nil
	case "t", "tib":
		return 1 << 40, nil
	case "tb":
		return 1_000_000_000_000, nil
	}
	return 0, fmt.Errorf("unknown unit %q", u)
}

// maxSizeFlag reads --max-size (0 = unlimited). Persistent on root; fallback to
// the library default if missing or wrong type so a forgotten inherit still bounds.
func maxSizeFlag(cmd *cobra.Command) int64 {
	if f := cmd.Flags().Lookup("max-size"); f != nil {
		if v, ok := f.Value.(*byteSizeValue); ok {
			return int64(*v)
		}
	}
	return wl.DefaultMaxSourceBytes
}
