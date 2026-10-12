package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// HashToken returns the hex-encoded SHA-256 of token, the only form in which
// the master stores or compares tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// MatchToken reports whether HashToken(candidate) equals one of hashes.
// Comparing fixed-length digests hides the token length, and every entry is
// compared so the timing does not reveal which one matched.
func MatchToken(hashes []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	candidateHash := []byte(HashToken(candidate))
	matched := 0
	for _, hash := range hashes {
		matched |= subtle.ConstantTimeCompare([]byte(hash), candidateHash)
	}
	return matched == 1
}
