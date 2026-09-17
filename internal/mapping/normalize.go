package mapping

import "strings"

// normalizeKey trims and uppercases a native tag name for read lookup. Separators are
// preserved (underscore != space). Shared by ID3, MP4, Matroska, and Vorbis read paths.
func normalizeKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
