package main

import "testing"

// TestNormalizeIssuerURL guards against a regression of the trailing-slash issuer mismatch
// found live against Authentik: go-oidc's NewProvider does a strict string comparison between
// the issuer URL passed to it and the "issuer" claim returned by the provider's discovery
// document, and Authentik always returns that claim with a trailing slash. Whatever we're
// configured with must normalize to that same convention.
func TestNormalizeIssuerURL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no trailing slash",
			input: "https://auth.ditch.family/application/o/obot",
			want:  "https://auth.ditch.family/application/o/obot/",
		},
		{
			name:  "already has trailing slash",
			input: "https://auth.ditch.family/application/o/obot/",
			want:  "https://auth.ditch.family/application/o/obot/",
		},
		{
			name:  "multiple trailing slashes",
			input: "https://auth.ditch.family/application/o/obot//",
			want:  "https://auth.ditch.family/application/o/obot/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeIssuerURL(tc.input); got != tc.want {
				t.Errorf("normalizeIssuerURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
