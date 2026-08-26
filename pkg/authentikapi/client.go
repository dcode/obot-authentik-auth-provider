// Package authentikapi is a minimal client for the parts of Authentik's REST API the auth
// provider needs: listing and resolving Directory > Groups, and reading a user's group
// memberships. It deliberately does not attempt to be a general-purpose Authentik SDK.
package authentikapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to a single Authentik instance's REST API using a static API token.
//
// Authentik has no workload-identity trust mechanism comparable to AWS IRSA or GCP Workload
// Identity: its own Management API only accepts a session cookie or one of its native API
// Tokens, not an arbitrary externally-issued JWT (such as a projected Kubernetes
// ServiceAccount token) and not the OAuth2 access tokens its own OAuth2/OIDC Providers issue
// to relying-party applications. A long-lived token scoped to a dedicated, read-only service
// account is therefore the credential this client is built around; see docs/configuration.md
// for how to scope one down.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client

	discoverOnce sync.Once
	userInfoURL  string
	discoverErr  error
}

// New builds a Client for the Authentik instance at baseURL (e.g. "https://auth.example.com"),
// authenticating API calls with apiToken.
func New(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Group is the subset of Authentik's Group model this client cares about.
type Group struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

type groupList struct {
	Pagination struct {
		// Next is the next page number, or 0 when the listing is exhausted.
		Next int `json:"next"`
	} `json:"pagination"`
	Results []Group `json:"results"`
}

// ListGroups fetches one page of Directory > Groups, optionally filtered by a
// case-insensitive substring match on name. page is 1-indexed; pass 0 for the first page.
// The returned nextPage is 0 when there are no more pages.
func (c *Client) ListGroups(ctx context.Context, nameFilter string, page, pageSize int) (groups []Group, nextPage int, err error) {
	q := url.Values{}
	q.Set("page_size", strconv.Itoa(pageSize))
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if nameFilter != "" {
		q.Set("search", nameFilter)
	}

	var list groupList
	if err := c.getJSON(ctx, "/api/v3/core/groups/?"+q.Encode(), &list); err != nil {
		return nil, 0, fmt.Errorf("failed to list groups: %w", err)
	}

	return list.Results, list.Pagination.Next, nil
}

// GetGroup resolves a single group by its Authentik primary key (UUID). It returns (nil, nil)
// if the group no longer exists, matching the "stale reference" behavior Obot expects when an
// access rule still points at a group that was since deleted in Authentik.
func (c *Client) GetGroup(ctx context.Context, id string) (*Group, error) {
	var group Group
	err := c.getJSON(ctx, "/api/v3/core/groups/"+url.PathEscape(id)+"/", &group)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch group %s: %w", id, err)
	}
	return &group, nil
}

type user struct {
	Groups []string `json:"groups"`
}

// GetUserGroupIDs returns the Authentik group PKs the given user belongs to. userID is
// Authentik's own user ID, i.e. the OIDC "sub" claim when the provider's Subject mode is
// "Based on the User's ID" -- see docs/configuration.md for why that mode is required.
func (c *Client) GetUserGroupIDs(ctx context.Context, userID string) ([]string, error) {
	var u user
	if err := c.getJSON(ctx, "/api/v3/core/users/"+url.PathEscape(userID)+"/", &u); err != nil {
		return nil, fmt.Errorf("failed to fetch user %s: %w", userID, err)
	}
	return u.Groups, nil
}

// DiscoverUserInfoEndpoint returns the userinfo_endpoint advertised by issuerURL's OIDC
// discovery document, caching it for the lifetime of the client. Authentik's userinfo path has
// changed across versions, so this discovers it rather than hardcoding it.
func (c *Client) DiscoverUserInfoEndpoint(ctx context.Context, issuerURL string) (string, error) {
	c.discoverOnce.Do(func() {
		discoveryURL := strings.TrimSuffix(issuerURL, "/") + "/.well-known/openid-configuration"

		var doc struct {
			UserInfoEndpoint string `json:"userinfo_endpoint"`
		}
		if err := c.getJSONFromURL(ctx, discoveryURL, &doc); err != nil {
			c.discoverErr = fmt.Errorf("failed to fetch OIDC discovery document at %s: %w", discoveryURL, err)
			return
		}
		if doc.UserInfoEndpoint == "" {
			c.discoverErr = fmt.Errorf("OIDC discovery document at %s has no userinfo_endpoint", discoveryURL)
			return
		}

		c.userInfoURL = doc.UserInfoEndpoint
	})

	return c.userInfoURL, c.discoverErr
}

// statusError carries the HTTP status code of a failed API call so callers can distinguish
// "not found" from other failures without string matching.
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("unexpected status %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.status == http.StatusNotFound
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.getJSONFromURL(ctx, c.baseURL+path, out)
}

func (c *Client) getJSONFromURL(ctx context.Context, fullURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		return &statusError{status: resp.StatusCode, body: string(body[:n])}
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}
