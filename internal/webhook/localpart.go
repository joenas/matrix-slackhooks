package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const maxSlugLength = 64
const hashLength = 8

func isAllowedRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("._=-", r)
}

// Localpart generates the Matrix user localpart for a webhook source name.
// The name is lowercased and stripped to the character set Matrix allows in
// bridged user IDs ([a-z0-9._=-]). A short hash of the original name is
// appended whenever the slug is not exactly the trimmed original name (any
// case change, separator substitution, dropped character or truncation), so
// names that would otherwise slug to the same localpart never collide. Only
// names that already are valid lowercase localparts stay hash-free.
func Localpart(prefix, name string) string {
	trimmed := strings.TrimSpace(name)
	s := slugify(trimmed)
	lossy := s != trimmed
	if len(s) > maxSlugLength {
		s = strings.Trim(s[:maxSlugLength], "-.")
		lossy = true
	}
	if lossy {
		if s != "" {
			s += "-"
		}
		s += shortHash(name)
	}
	if s == "" {
		s = "webhook"
	}
	return prefix + s
}

// slugify lowercases the name and maps every character outside the allowed
// set to a single "-" separator, trimming leading/trailing separators.
func slugify(name string) string {
	var slug strings.Builder
	prevSeparator := true
	for _, r := range strings.ToLower(name) {
		if isAllowedRune(r) {
			slug.WriteRune(r)
			prevSeparator = false
		} else if !prevSeparator {
			slug.WriteByte('-')
			prevSeparator = true
		}
	}
	return strings.Trim(slug.String(), "-.")
}

func shortHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:hashLength]
}
