package security

func ValidateToken(allowedTokens []string, candidate string) bool {
	for _, token := range allowedTokens {
		if token == candidate {
			return true
		}
	}
	return false
}
