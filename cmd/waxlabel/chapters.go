package main

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// splitChapter parses an --add-chapter "TIMESTAMP=Title" assignment: the timestamp
// before the first '=', and the (possibly empty, possibly '='-containing) title
// after it. It mirrors splitAssign, so the value side is taken verbatim - a title
// may contain '=' or be empty. A missing '=' or a malformed timestamp is a usage
// error.
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

// parseChapterTimestamp parses a chapter start written as [H:]MM:SS[.mmm] or as
// bare (possibly fractional) seconds. It round-trips the dump format exactly:
// chapterTimestamp emits H:MM:SS.mmm, so a timestamp copied from a dump line parses
// back to the same instant. Components are colon-separated and the seconds field
// may carry 1, 2, or 3 fractional digits. Fractions are millisecond-resolution:
// ".5" and ".500" are valid; ".9999" and a dangling "." are not. The leading
// component may exceed 60 (90:00 is ninety minutes) but is bounded by the
// representable range; an inner minutes field and the seconds field are each < 60,
// and every field is non-negative - a negative,
// out-of-range, or non-numeric field is a usage error.
func parseChapterTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	// Restrict to the decimal grammar before parsing: strconv.ParseFloat would
	// otherwise accept hex ("0x1p4"), scientific ("1e3"), underscored ("1_000"), and
	// signed ("+90") forms, and ParseInt a leading '+', all outside the documented
	// [H:]MM:SS[.mmm] shape. Allow only digits, the ':' separators, and the seconds
	// '.'; this also subsumes the Inf/NaN guard (letters are rejected here).
	if !onlyTimestampBytes(s) {
		return 0, badTimestamp(s)
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, badTimestamp(s)
	}
	// The last component is the seconds (fractional allowed); any preceding ones are
	// whole hours/minutes.
	secStr := parts[len(parts)-1]
	// When a fractional part is present it must be 1 to 3 digits: ParseFloat would
	// otherwise accept a dangling dot ("1:30.") and an over-precise fraction (".9999"),
	// neither of which the documented millisecond grammar admits. A bare ".5" stays valid.
	if idx := strings.IndexByte(secStr, '.'); idx >= 0 {
		if frac := secStr[idx+1:]; len(frac) == 0 || len(frac) > 3 {
			return 0, badTimestamp(s)
		}
	}
	secs, err := strconv.ParseFloat(secStr, 64)
	if err != nil || secs < 0 {
		return 0, badTimestamp(s)
	}
	// Seconds is a bounded (non-leading) field whenever there is more than one
	// component; only the single bare-seconds component is unbounded.
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
		// Bare seconds: the only (leading) component, so it is unbounded.
	}
	// Reject a magnitude past what an int64-nanosecond time.Duration can hold
	// (~292 years): an absurd field (e.g. a millions-of-hours leading component, or
	// a huge bare-seconds value) would otherwise wrap to a negative duration,
	// silently violating the non-negative contract above. The float sum is only
	// approximate near the int64 ceiling, but at this scale the error is nanoseconds
	// against centuries; the d < 0 guard then catches any boundary leak from that
	// rounding (every field is non-negative, so a negative result can only be overflow).
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

// onlyTimestampBytes reports whether s is built solely from the decimal-timestamp
// alphabet: ASCII digits, the ':' component separator, and the '.' seconds point.
// It is the gate that keeps the strconv parsers from accepting hex/scientific/
// underscored/signed numeric forms outside the documented grammar. An empty string
// fails (no digits), which the parsers would reject anyway.
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

// parseTimeField parses a whole-number hours/minutes component. A limit > 0 bounds
// it to [0, limit) - an inner field that must not overflow into the next; limit <= 0
// leaves it unbounded above, for a leading field. It reports ok=false for a
// non-numeric, negative, or out-of-range value.
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
