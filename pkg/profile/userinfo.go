package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/dcode/obot-authentik-auth-provider/pkg/authentikapi"
)

// UserInfo is the response shape GET /obot-get-user-info returns, matching the reference
// providers (github-auth-provider, okta-auth-provider) rather than docs/auth-providers.md's
// JSONSchema, which describes /obot-get-state instead -- the two endpoints serve different
// purposes and the reference implementations are the authoritative contract for this one.
type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// GetUserInfo calls the Authentik application's OIDC userinfo endpoint with the caller's own
// access token (as opposed to the service API token used for group-directory lookups) and
// returns basic profile information.
func GetUserInfo(ctx context.Context, client *authentikapi.Client, issuerURL, accessToken string) (*UserInfo, error) {
	endpoint, err := client.DiscoverUserInfoEndpoint(ctx, issuerURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call userinfo endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var claims struct {
		Sub               string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}

	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}

	return &UserInfo{
		ID:   claims.Sub,
		Name: name,
		// Authentik's userinfo response has no standardized avatar URL claim.
		IconURL: "",
	}, nil
}
