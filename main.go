// Command obot-provider is Obot's auth provider for Authentik: an OIDC login backed by a
// forked oauth2-proxy, plus a group-directory API used to list and resolve Authentik groups for
// Obot's access-control UI. See docs/ for setup and the Authentik-side configuration it expects.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	oauth2proxy "github.com/oauth2-proxy/oauth2-proxy/v7"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/validation"
	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/env"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"

	"github.com/dcode/obot-authentik-auth-provider/pkg/authentikapi"
	"github.com/dcode/obot-authentik-auth-provider/pkg/profile"
)

// providerKind namespaces group IDs and cursors so they can never be confused with another
// provider's. It must match groupIDPrefix (minus the trailing slash) in
// auth-providers/authentik-auth-provider.yaml.
const providerKind = "authentik"

// version is set via -ldflags at release build time (see .goreleaser.yaml); it stays "dev" for
// local builds.
var version = "dev"

// normalizeIssuerURL returns issuer with exactly one trailing slash, matching Authentik's own
// convention for the "issuer" claim in its OIDC discovery document. See the comment where this
// is called in run() for why matching that convention (rather than stripping the slash) is what
// go-oidc's issuer-equality check requires.
func normalizeIssuerURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/"
}

// buildRedirectURL returns the absolute callback URL oauth2-proxy should register with the
// upstream IdP as its "redirect_uri". oauth2-proxy only auto-appends its callback path
// (proxyPrefix + "/callback") onto RawRedirectURL when that value's URL.Path is empty -- a bare
// "https://host/" (note the trailing slash) already has a non-empty Path ("/"), so oauth2-proxy
// treats it as a complete, final redirect URI and uses it verbatim. That was exactly the bug
// found live: Authentik's OAuth2 provider only allows the "/oauth2/callback" redirect URI
// (strict matching), so it rejected the bare-root value with a "Redirect URI Error". Building
// the full path ourselves, tied to the actual configured proxyPrefix rather than a hardcoded
// literal, avoids relying on oauth2-proxy's empty-path auto-append behavior at all.
func buildRedirectURL(serverURL, proxyPrefix string) string {
	return strings.TrimRight(serverURL, "/") + proxyPrefix + "/callback"
}

type Options struct {
	ClientID     string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_ID"`
	ClientSecret string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_SECRET"`
	IssuerURL    string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_ISSUER_URL"`

	// GroupsClaim names the ID token claim carrying the user's group names. It defaults to
	// "groups", but can point at an existing custom scope/claim mapping instead (e.g. a
	// "k8s_groups" mapping already used by other applications) so no new mapping is required.
	GroupsClaim string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_GROUPS_CLAIM" optional:"true" default:"groups"`

	// APIURL and APIToken back the group-directory endpoints (list/resolve groups, look up a
	// user's memberships). Both are optional at the binary level so login-only operation keeps
	// working without them; the manifest marks them required for a default deployment.
	APIURL   string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_API_URL" optional:"true"`
	APIToken string `env:"OBOT_AUTHENTIK_AUTH_PROVIDER_API_TOKEN" optional:"true"`

	ObotServerURL                     string `env:"OBOT_SERVER_PUBLIC_URL,OBOT_SERVER_URL"`
	PostgresConnectionDSN             string `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN" optional:"true"`
	PostgresMaxConnections            int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS" optional:"true"`
	PostgresMaxIdleConnections        int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS" optional:"true"`
	PostgresConnectionLifetimeSeconds int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS" optional:"true"`
	AuthCookieSecret                  string `env:"OBOT_AUTH_PROVIDER_COOKIE_SECRET"`
	AuthEmailDomains                  string `env:"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS" default:"*"`
	AuthTokenRefreshDuration          string `env:"OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION" optional:"true" default:"1h"`
	LoggingEnabled                    string `env:"OBOT_AUTH_PROVIDER_ENABLE_LOGGING" optional:"true"`
}

type server struct {
	apiClient *authentikapi.Client
	issuerURL string
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("ERROR: authentik-auth-provider: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var opts Options
	if err := env.LoadEnvForStruct(&opts); err != nil {
		return fmt.Errorf("failed to load options: %w", err)
	}

	// Authentik's OIDC discovery document always returns its "issuer" claim with a trailing
	// slash (e.g. "https://auth.example.com/application/o/obot/"), regardless of how the
	// issuer is configured on our side. go-oidc's NewProvider does a strict string comparison
	// between the issuer URL it was given and the issuer claim returned by discovery, with no
	// normalization of its own -- so any mismatch here (extra slash, missing slash, double
	// slash) breaks OIDC discovery with "issuer did not match the issuer returned by
	// provider". Normalize to Authentik's own convention (exactly one trailing slash) instead
	// of stripping it, so this matches regardless of whether the configured value has zero,
	// one, or more trailing slashes.
	opts.IssuerURL = normalizeIssuerURL(opts.IssuerURL)

	refreshDuration, err := time.ParseDuration(opts.AuthTokenRefreshDuration)
	if err != nil {
		return fmt.Errorf("failed to parse token refresh duration: %w", err)
	}
	if refreshDuration < 0 {
		return errors.New("token refresh duration must be greater than 0")
	}

	cookieSecret, err := base64.StdEncoding.DecodeString(opts.AuthCookieSecret)
	if err != nil {
		return fmt.Errorf("failed to decode cookie secret: %w", err)
	}

	legacyOpts := options.NewLegacyOptions()
	legacyOpts.LegacyProvider.ProviderType = "oidc"
	legacyOpts.LegacyProvider.ProviderName = "oidc"
	legacyOpts.LegacyProvider.ClientID = opts.ClientID
	legacyOpts.LegacyProvider.ClientSecret = opts.ClientSecret
	legacyOpts.LegacyProvider.OIDCIssuerURL = opts.IssuerURL
	legacyOpts.LegacyProvider.OIDCGroupsClaim = opts.GroupsClaim
	legacyOpts.LegacyProvider.Scope = "openid email profile offline_access groups"

	oauthProxyOpts, err := legacyOpts.ToOptions()
	if err != nil {
		return fmt.Errorf("failed to convert legacy options to new options: %w", err)
	}

	oauthProxyOpts.Server.BindAddress = ""
	oauthProxyOpts.MetricsServer.BindAddress = ""
	if opts.PostgresConnectionDSN != "" {
		oauthProxyOpts.Session.Type = options.PostgresSessionStoreType
		oauthProxyOpts.Session.Postgres.ConnectionDSN = opts.PostgresConnectionDSN
		oauthProxyOpts.Session.Postgres.MaxOpenConns = opts.PostgresMaxConnections
		oauthProxyOpts.Session.Postgres.MaxIdleConns = opts.PostgresMaxIdleConnections
		oauthProxyOpts.Session.Postgres.ConnMaxLifetime = opts.PostgresConnectionLifetimeSeconds
		oauthProxyOpts.Session.Postgres.TableNamePrefix = providerKind + "_"
	}
	oauthProxyOpts.Cookie.Refresh = refreshDuration
	oauthProxyOpts.Cookie.Name = "obot_access_token"
	oauthProxyOpts.Cookie.Secret = string(cookieSecret)
	oauthProxyOpts.Cookie.Secure = strings.HasPrefix(opts.ObotServerURL, "https://")
	oauthProxyOpts.RawRedirectURL = buildRedirectURL(opts.ObotServerURL, oauthProxyOpts.ProxyPrefix)
	if opts.AuthEmailDomains != "" {
		emailDomains := strings.Split(opts.AuthEmailDomains, ",")
		for i := range emailDomains {
			emailDomains[i] = strings.TrimSpace(emailDomains[i])
		}
		oauthProxyOpts.EmailDomains = emailDomains
	}

	loggingEnabled := strings.EqualFold(opts.LoggingEnabled, "true")
	oauthProxyOpts.Logging.RequestEnabled = loggingEnabled
	oauthProxyOpts.Logging.AuthEnabled = loggingEnabled
	oauthProxyOpts.Logging.StandardEnabled = loggingEnabled

	if err := validation.Validate(oauthProxyOpts); err != nil {
		return fmt.Errorf("failed to validate options: %w", err)
	}

	oauthProxy, err := oauth2proxy.NewOAuthProxy(oauthProxyOpts, oauth2proxy.NewValidator(oauthProxyOpts.EmailDomains, oauthProxyOpts.AuthenticatedEmailsFile))
	if err != nil {
		return fmt.Errorf("failed to create oauth2 proxy: %w", err)
	}

	srv := &server{
		apiClient: authentikapi.New(opts.APIURL, opts.APIToken),
		issuerURL: opts.IssuerURL,
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}
	listenHost := os.Getenv("OBOT_PROVIDER_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	addr := net.JoinHostPort(listenHost, port)

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fmt.Appendf(nil, "http://%s", addr))
	})
	mux.HandleFunc("/obot-get-state", getState(oauthProxy))
	mux.HandleFunc("/obot-get-user-info", srv.getUserInfo)
	mux.HandleFunc("/obot-list-auth-groups", authcommon.ListGroupsHandler(providerKind, srv.fetchGroupPage))
	mux.HandleFunc("/obot-get-auth-groups", authcommon.GetGroupsHandler(providerKind, srv.fetchGroupsByIDs))
	mux.HandleFunc("/obot-list-user-auth-groups", srv.listUserGroups)
	mux.HandleFunc("/", oauthProxy.ServeHTTP)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("authentik-auth-provider %s listening on %s\n", version, addr)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to listen and serve: %w", err)
	}
	return nil
}

func (s *server) fetchGroupPage(ctx context.Context, req authcommon.PageRequest) (authcommon.PageResult, error) {
	return profile.FetchGroupPage(ctx, s.apiClient, req)
}

func (s *server) fetchGroupsByIDs(ctx context.Context, ids []string) (state.GroupInfoList, error) {
	return profile.FetchGroupsByIDs(ctx, s.apiClient, ids)
}

// listUserGroups returns all groups the specified user belongs to. Accepts a plain-text user ID
// in the request body, matching okta-auth-provider's contract.
func (s *server) listUserGroups(w http.ResponseWriter, r *http.Request) {
	userIDBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	userID := strings.TrimSpace(string(userIDBytes))
	if userID == "" {
		http.Error(w, "user ID is required in request body", http.StatusBadRequest)
		return
	}

	groups, err := profile.FetchUserGroupInfos(r.Context(), s.apiClient, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch user groups: %v", err), http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = state.GroupInfoList{}
	}

	if err := json.NewEncoder(w).Encode(groups); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
	}
}

// getState wraps auth-providers-common's state.ObotGetState, overriding the response's "user"
// field to be human-readable instead of Authentik's raw numeric sub.
//
// Per obot-platform/providers's docs/auth-providers.md, "/obot-get-state" is what Obot calls on
// every request to identify the caller, and its response's "user" field ("the identifier for the
// user") is what Obot persists as the account's username/display name -- confirmed live by
// decrypting a real obot_access_token cookie via this provider's own "/obot-get-state" endpoint
// and cross-referencing Obot's Postgres "identities" table, where "provider_username" held the
// literal numeric Authentik user ID.
//
// That numeric value is unavoidable for the *session's* underlying "sub" claim: sub_mode =
// "user_id" in authentik-obot.tf is required so the group-directory endpoints below can resolve
// group membership via Authentik's "/api/v3/core/users/{sub}/". oauth2-proxy's ID-token "User"
// claim always mirrors "sub" for OIDC-type providers -- this fork's provider setup
// (providers.go's newProviderDataFromConfig) has no option that changes which claim populates
// SessionState.User, so it is always "sub", unconfigurably.
//
// That's session-internal, though, and separate from what this handler reports in the response
// body: state.GetSerializableState reads the already-established, unmodified session (still
// backed by the real signed ID/access tokens Authentik issued, "sub" claim and all) to build
// accessToken/idToken/groups, and only *after* that is done does this handler substitute a
// friendlier value into the response's "user" field. Nothing here touches the signed tokens
// themselves or what any other endpoint (e.g. /obot-list-user-auth-groups) reads out of them, so
// group-directory lookups are unaffected.
func getState(oauthProxy *oauth2proxy.OAuthProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sr state.SerializableRequest
		if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
			http.Error(w, fmt.Sprintf("failed to decode request body: %v", err), http.StatusBadRequest)
			return
		}

		reqObj, err := http.NewRequest(sr.Method, sr.URL, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create request object: %v", err), http.StatusBadRequest)
			return
		}
		reqObj.Header = sr.Header

		ss, err := state.GetSerializableState(oauthProxy, reqObj)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to get state: %v", err), http.StatusInternalServerError)
			return
		}

		ss.User = humanReadableUser(ss)

		if err := json.NewEncoder(w).Encode(ss); err != nil {
			http.Error(w, fmt.Sprintf("failed to encode state: %v", err), http.StatusInternalServerError)
		}
	}
}

// humanReadableUser picks the value getState should report as "user": PreferredUsername (backed
// by Authentik's "preferred_username" claim, which the default "profile" scope mapping always
// sets to request.user.username -- see docs/configuration.md) when present, falling back to
// Email, and only falling back to the session's raw (numeric, for Authentik) User value if
// neither is available.
func humanReadableUser(ss state.SerializableState) string {
	if ss.PreferredUsername != "" {
		return ss.PreferredUsername
	}
	if ss.Email != "" {
		return ss.Email
	}
	return ss.User
}

func (s *server) getUserInfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		http.Error(w, "no authorization token provided", http.StatusUnauthorized)
		return
	}

	u, err := profile.GetUserInfo(r.Context(), s.apiClient, s.issuerURL, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get user: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(u); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode user info: %v", err), http.StatusInternalServerError)
	}
}
