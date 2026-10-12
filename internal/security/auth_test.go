package security

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHashTokenReturnsHexSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("alpha"))

	got := HashToken("alpha")

	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("HashToken(alpha)=%q want %q", got, want)
	}
}

func TestMatchTokenComparesCandidateHashAgainstEveryHash(t *testing.T) {
	hashes := []string{HashToken("alpha"), HashToken("beta")}
	cases := []struct {
		name      string
		hashes    []string
		candidate string
		want      bool
	}{
		{"match first", hashes, "alpha", true},
		{"match second", hashes, "beta", true},
		{"no match", hashes, "gamma", false},
		{"raw token is not a hash", []string{"alpha"}, "alpha", false},
		{"malformed stored hash", []string{"abc"}, "abc", false},
		{"empty hashes", nil, "alpha", false},
		{"empty candidate never matches", []string{HashToken("")}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchToken(tc.hashes, tc.candidate); got != tc.want {
				t.Fatalf("MatchToken(%q)=%v want %v", tc.candidate, got, tc.want)
			}
		})
	}
}
