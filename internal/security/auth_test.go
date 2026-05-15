package security

import "testing"

func TestValidateToken(t *testing.T) {
	cases := []struct {
		name      string
		allowed   []string
		candidate string
		want      bool
	}{
		{"match first", []string{"alpha", "beta"}, "alpha", true},
		{"match second", []string{"alpha", "beta"}, "beta", true},
		{"no match", []string{"alpha", "beta"}, "gamma", false},
		{"empty allowed", []string{}, "alpha", false},
		{"empty candidate", []string{"alpha"}, "", false},
		{"empty both", []string{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateToken(tc.allowed, tc.candidate)
			if got != tc.want {
				t.Fatalf("ValidateToken(%v, %q)=%v want %v", tc.allowed, tc.candidate, got, tc.want)
			}
		})
	}
}
