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

	opts.IssuerURL = strings.TrimSuffix(opts.IssuerURL, "/")

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
	oauthProxyOpts.RawRedirectURL = opts.ObotServerURL + "/"
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
	mux.HandleFunc("/obot-get-state", state.ObotGetState(oauthProxy))
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
