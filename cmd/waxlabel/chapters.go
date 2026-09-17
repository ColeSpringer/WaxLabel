package main

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// splitChapter parses --add-chapter "TIMESTAMP=Title": timestamp before the first
// '=', title (possibly empty or containing '=') after. Mirrors splitAssign: value
// side is verbatim. Missing '=' or bad timestamp is a usage error.
func splitChapter(s string) (start time.Duration, title string, err error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return 0, "", usagef("missing '=' in %q (want TIMESTAMP=Title, e.g. 1:30=Verse)", s)
	}
	start, err = parseChapterTimestamp(s[:i])
	if err != nil {
		return 0, "", err
	}
	title = s[i+1:]
	if err := checkArgText(title, "chapter title"); err != nil {
		return 0, "", err
	}
	return start, title, nil
}

// parseChapterTimestamp parses [H:]MM:SS[.mmm] or bare (possibly fractional)
// seconds. Round-trips dump's H:MM:SS.mmm. Fractional seconds are 1-3 digits
// (ms resolution). Leading component may exceed 60 (90:00 = ninety minutes) but
// stays in representable range; inner minutes and seconds are each < 60; all
// fields non-negative.
func parseChapterTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	// Restrict to the decimal grammar: strconv would otherwise accept hex,
	// scientific, underscores, and signs outside [H:]MM:SS[.mmm]. Digits, ':',
	// and '.' only; also rejects Inf/NaN.
	if !onlyTimestampBytes(s) {
		return 0, badTimestamp(s)
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, badTimestamp(s)
	}
	secStr := parts[len(parts)-1]
	// Fractional part must be 1-3 digits: ParseFloat accepts a dangling "." and
	// over-precise fractions, which the ms grammar does not.
	if idx := strings.IndexByte(secStr, '.'); idx >= 0 {
		if frac := secStr[idx+1:]; len(frac) == 0 || len(frac) > 3 {
			return 0, badTimestamp(s)
		}
	}
	secs, err := strconv.ParseFloat(secStr, 64)
	if err != nil || secs < 0 {
		return 0, badTimestamp(s)
	}
	// Seconds is bounded when multi-component; bare seconds alone is unbounded.
	if len(parts) >= 2 && secs >= 60 {
		return 0, badTimestamp(s)
	}
	var hours, mins int64
	switch len(parts) {
	case 3:
		var ok bool
		if hours, ok = parseTimeField(parts[0], 0); !ok { // leading hours: unbounded
			return 0, badTimestamp(s)
		}
		if mins, ok = parseTimeField(parts[1], 60); !ok { // inner minutes: < 60
			return 0, badTimestamp(s)
		}
	case 2:
		var ok bool
		if mins, ok = parseTimeField(parts[0], 0); !ok { // leading minutes: unbounded
			return 0, badTimestamp(s)
		}
	case 1:
		// Bare seconds: only component, unbounded.
	}
	// Reject magnitudes past int64-nanosecond Duration (~292 years); otherwise
	// a huge field wraps to a negative duration. Float sum is approximate near
	// the ceiling; d < 0 catches boundary leak (all fields non-negative).
	if float64(hours)*float64(time.Hour)+float64(mins)*float64(time.Minute)+secs*float64(time.Second) >= float64(math.MaxInt64) {
		return 0, badTimestamp(s)
	}
	d := time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute +
		time.Duration(math.Round(secs*float64(time.Second)))
	if d < 0 {
		return 0, badTimestamp(s)
	}
	return d, nil
}

// onlyTimestampBytes reports whether s uses only digits, ':', and '.'. Gates
// strconv away from hex/scientific/underscore/signed forms. Empty fails.
func onlyTimestampBytes(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b < '0' || b > '9') && b != ':' && b != '.' {
			return false
		}
	}
	return true
}

// parseTimeField parses a whole hours/minutes component. limit > 0 bounds to
// [0, limit); limit <= 0 is unbounded above (leading field). ok=false on
// non-numeric, negative, or out-of-range.
func parseTimeField(s string, limit int64) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || (limit > 0 && n >= limit) {
		return 0, false
	}
	return n, true
}

// badTimestamp formats the usage error for a malformed chapter timestamp.
func badTimestamp(s string) error {
	return usagef("invalid chapter timestamp %q (want [H:]MM:SS[.mmm] or seconds, e.g. 1:30 or 90)", s)
}
