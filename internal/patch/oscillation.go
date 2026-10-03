package patch

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// EditIdentity detects an exact reversal independent of hunk offsets, context,
// Git metadata, or filesystem modes. It is only used after patch validation.
func EditIdentity(diff string, reverse bool) string {
	var removed, added strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- a/"):
			name := strings.TrimPrefix(line, "--- a/")
			removed.WriteString("file:" + name + "\n")
			added.WriteString("file:" + name + "\n")
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "-"):
			removed.WriteString(line[1:] + "\n")
		case strings.HasPrefix(line, "+"):
			added.WriteString(line[1:] + "\n")
		}
	}
	a, b := removed.String(), added.String()
	if reverse {
		a, b = b, a
	}
	h := sha256.Sum256([]byte(a + "\x00" + b))
	return hex.EncodeToString(h[:])
}
