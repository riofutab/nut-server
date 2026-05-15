package security

import "crypto/subtle"

func ValidateToken(allowedTokens []string, candidate string) bool {
	candidateBytes := []byte(candidate)
	matched := false
	for _, token := range allowedTokens {
		if subtle.ConstantTimeCompare([]byte(token), candidateBytes) == 1 {
			matched = true
		}
	}
	return matched
}
