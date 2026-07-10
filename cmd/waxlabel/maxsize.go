package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// byteSizeValue is the pflag.Value backing --max-size. It parses a human-readable byte
// size (a bare count, or a number with a binary or decimal unit) into a byte ceiling and
// renders it back with the library's HumanBytes formatter, so the help text and any
// error echo the size the same way WaxLabel prints every other size. A stored value of 0
// means "unlimited". WaxLabel already formats sizes with HumanBytes but has no parser to
// reuse, so this type owns the reverse direction.
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

// parseByteSize converts a human byte-size string into a byte count. It accepts a bare
// integer or decimal number, optionally followed by a unit: KB/MB/GB/TB are decimal
// (powers of 1000) and KiB/MiB/GiB/TiB are binary (powers of 1024), matched
// case-insensitively with an optional space before the unit. A bare unit letter (K, M,
// G, T) is binary, so a value round-trips with HumanBytes' binary output. "0" (any zero)
// means unlimited. A redundant leading '+' is accepted; a negative, empty, or unparseable
// value is rejected (a leading '-' reports "must not be negative").
func parseByteSize(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	// A leading '-' is a negative size: name it as such. The digit-only number scan below never
	// consumes the sign, so without this check "-5MB" would fall through to the generic
	// "expected a leading number" and the num < 0 branch would be dead. A leading '+' is a
	// redundant positive sign; strip it so "+5MB" parses like "5MB".
	if strings.HasPrefix(trimmed, "-") {
		return 0, fmt.Errorf("invalid size %q: must not be negative", s)
	}
	trimmed = strings.TrimPrefix(trimmed, "+")
	// Split the leading number (digits and an optional decimal point) from the unit.
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
	total := num * float64(mult)
	// float64(math.MaxInt64) rounds up to 2^63, so this comparison also rejects a total of
	// exactly 2^63, which int64(total) would otherwise wrap to a negative "unlimited" value.
	if total >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return int64(total), nil
}

// unitMultiplier maps a size unit suffix to its byte multiplier. An empty suffix or a bare
// "B" is one byte; a bare letter (K/M/G/T) or an explicit "iB" form is binary (1024), and a
// plain decimal "B" form (KB/MB/...) is 1000. The forms are listed explicitly rather than
// derived from suffix trimming so the accepted set is obvious. Binary units match the
// binary magnitudes HumanBytes prints.
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

// maxSizeFlag reads the resolved --max-size ceiling for a command (0 means unlimited).
// The flag is registered persistently on the root, so every stdin-reading subcommand
// resolves it through this one helper rather than redeclaring the flag. A missing flag or
// an unexpected value type falls back to the library default, so a command that forgets
// to inherit it still ingests within a bound.
func maxSizeFlag(cmd *cobra.Command) int64 {
	if f := cmd.Flags().Lookup("max-size"); f != nil {
		if v, ok := f.Value.(*byteSizeValue); ok {
			return int64(*v)
		}
	}
	return wl.DefaultMaxSourceBytes
}
