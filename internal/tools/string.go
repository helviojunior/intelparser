package tools

import (
	"strings"
	"unicode/utf8"
)

// LeftTrucate a string if its more than max
func LeftTrucate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	return s[max:]
}

// SanitizeUTF8 removes null bytes and invalid UTF-8 sequences from a string,
// ensuring it is safe for PostgreSQL UTF-8 encoding.
func SanitizeUTF8(s string) string {
	// Remove null bytes (0x00) - PostgreSQL does not accept them in text fields
	s = strings.ReplaceAll(s, "\x00", "")

	// If the string is already valid UTF-8, return it
	if utf8.ValidString(s) {
		return s
	}

	// Replace invalid UTF-8 sequences with the Unicode replacement character
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Skip invalid byte
			i++
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}
