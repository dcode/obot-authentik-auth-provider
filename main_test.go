package main

import (
	"testing"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

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

// TestBuildRedirectURL guards against a regression of the bare-root redirect_uri bug found live
// against Authentik: the provider's OAuth2 config only allows "/oauth2/callback" as a redirect
// URI under strict matching, so RawRedirectURL must always carry that full path rather than a
// bare "https://host/" that oauth2-proxy would otherwise pass through verbatim.
func TestBuildRedirectURL(t *testing.T) {
	cases := []struct {
		name        string
		serverURL   string
		proxyPrefix string
		want        string
	}{
		{
			name:        "no trailing slash on server URL",
			serverURL:   "https://obot.ditch.family",
			proxyPrefix: "/oauth2",
			want:        "https://obot.ditch.family/oauth2/callback",
		},
		{
			name:        "trailing slash on server URL",
			serverURL:   "https://obot.ditch.family/",
			proxyPrefix: "/oauth2",
			want:        "https://obot.ditch.family/oauth2/callback",
		},
		{
			name:        "custom proxy prefix",
			serverURL:   "https://obot.ditch.family",
			proxyPrefix: "/custom-prefix",
			want:        "https://obot.ditch.family/custom-prefix/callback",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildRedirectURL(tc.serverURL, tc.proxyPrefix); got != tc.want {
				t.Errorf("buildRedirectURL(%q, %q) = %q, want %q", tc.serverURL, tc.proxyPrefix, got, tc.want)
			}
		})
	}
}

// TestHumanReadableUser guards against a regression of the literal-numeric-sub-as-username bug
// found live: Authentik's sub_mode is set to the user's numeric ID (required for the
// group-directory endpoints to resolve /api/v3/core/users/{sub}/), and oauth2-proxy's
// SessionState.User field always mirrors that "sub" claim -- there is no option in this
// codebase's oauth2-proxy fork to point it at a different claim. Since Obot persists
// /obot-get-state's "user" field verbatim as the account's username (confirmed live via Obot's
// Postgres "identities" table), that numeric sub ended up as the literal displayed username. This
// preference order (PreferredUsername, then Email, then the raw User value) is what getState
// applies before responding.
func TestHumanReadableUser(t *testing.T) {
	cases := []struct {
		name string
		ss   state.SerializableState
		want string
	}{
		{
			name: "prefers preferred username",
			ss: state.SerializableState{
				User:              "4",
				PreferredUsername: "akadmin",
				Email:             "root@ditch.family",
			},
			want: "akadmin",
		},
		{
			name: "falls back to email when preferred username is empty",
			ss: state.SerializableState{
				User:  "6",
				Email: "dcode@ditch.family",
			},
			want: "dcode@ditch.family",
		},
		{
			name: "falls back to the raw user value when nothing else is available",
			ss: state.SerializableState{
				User: "6",
			},
			want: "6",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanReadableUser(tc.ss); got != tc.want {
				t.Errorf("humanReadableUser(%+v) = %q, want %q", tc.ss, got, tc.want)
			}
		})
	}
}
