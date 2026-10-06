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
// remote bridged user IDs ([a-z0-9._=-]). A short hash of the original name
// is appended when the slug was lossy (non-ASCII characters dropped or the
// slug truncated), so different names never collide.
func Localpart(prefix, name string) string {
	var slug strings.Builder
	lossy := false
	prevSeparator := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if isAllowedRune(r) {
			slug.WriteRune(r)
			prevSeparator = false
			continue
		}
		if r > 127 {
			lossy = true
		}
		if !prevSeparator {
			slug.WriteByte('-')
			prevSeparator = true
		}
	}
	s := strings.Trim(slug.String(), "-.")
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

func shortHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:hashLength]
}
