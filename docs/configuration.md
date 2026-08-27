# Configuration

## Authentik: OAuth2/OpenID Provider and Application

1. In Authentik, create an **OAuth2/OpenID Provider** (Applications > Providers > Create):
   - **Client type**: Confidential
   - **Redirect URIs**: `<your Obot server URL>/oauth2/callback`
   - **Subject mode**: **Based on the User's ID**. This is required, not a preference -- the
     provider's group-directory endpoints look up a user's memberships by calling Authentik's
     `/api/v3/core/users/{id}/` with the OIDC `sub` claim as `{id}`, which only lines up when the
     subject is the numeric/UUID user ID rather than a hashed or username-based subject. This
     doesn't mean Obot's UI shows raw numeric IDs as usernames -- as of v0.1.3, the provider
     separately surfaces a human-readable identifier (preferred username, falling back to email)
     for display, while group lookups keep reading the numeric `sub` server-side. If you're
     running an older release, upgrade rather than working around this.
   - **Scopes**: at minimum `openid`, `email`, `profile`, `offline_access`, plus a scope that
     maps to a `groups` claim (see below).
2. Create an **Application** using that provider, and note the provider's **Client ID** and
   **Client Secret**.
3. Find the application's OIDC issuer URL under the provider's **OpenID Configuration Issuer**
   link (typically `https://<your-authentik-host>/application/o/<app-slug>/`).

### Groups claim

oauth2-proxy reads group membership directly off the ID token, so the OAuth2 Provider's scopes
need a mapping that adds a `groups` claim. If you don't already have one:

1. **Customization > Property Mappings > Create > Scope Mapping**
   - **Scope name**: `groups`
   - **Expression**: `return {"groups": [group.name for group in user.ak_groups.all()]}`
2. Add that scope mapping to the OAuth2 Provider's **Scopes**.

If you already have a similar mapping under a different claim name for another application, you
don't need a second one -- just point `OBOT_AUTHENTIK_AUTH_PROVIDER_GROUPS_CLAIM` at the existing
claim name instead.

### Roles and permissions

Authentik group membership (via the `groups` claim above) is what backs Obot's access-control
UI -- which groups can see/use which agents, MCP servers, etc. It is **not** what grants the
Owner or Admin role inside Obot itself. Confirmed live: a user in an Authentik group that a
deployment treats as "the admins group" still logs in with Obot's default role, identical to a
user in no groups at all. Obot elevates specific people to Owner/Admin through its own
`--auth-owner-emails`/`--auth-admin-emails` server flags (`OBOT_SERVER_AUTH_OWNER_EMAILS`/
`OBOT_SERVER_AUTH_ADMIN_EMAILS` per `obot server --help`) -- a plain email allowlist, unrelated to
this provider or to Authentik groups. These are also pflag string-slice flags, the same type as
`--provider-registries` above, which is confirmed dead as an env var in this codebase -- treat
the env var form of these with the same suspicion and prefer setting them as literal CLI args
unless you've verified otherwise on your Obot version.

## Authentik: service account for the group directory

The group-*directory* endpoints (browsing/searching all groups, resolving IDs, looking up a
user's memberships outside of an active login) call Authentik's core Management API directly,
which needs its own credential -- see [why this can't be a Kubernetes-native
identity](index.md#why-this-exists) or an OAuth2 access token from the login flow above.

1. Create a dedicated Authentik user for this purpose (not a real person's account).
2. Grant it read-only access to `Group` and `User` objects -- via a custom Role scoped to those
   two models under **Directory > Roles**, or, if you're not using Authentik's granular RBAC,
   an existing least-privilege account. Avoid handing this credential full admin rights; it never
   needs to write anything.
3. **Directory > Tokens > Create**, owned by that user, with no expiry (or a long one you're
   prepared to rotate).

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_ID` | Yes | Client ID of the Authentik OAuth2/OpenID Provider. |
| `OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_SECRET` | Yes | Client secret of the same provider. |
| `OBOT_AUTHENTIK_AUTH_PROVIDER_ISSUER_URL` | Yes | The application's OIDC issuer URL. |
| `OBOT_AUTHENTIK_AUTH_PROVIDER_API_URL` | Yes | Root URL of the Authentik instance (no path), used for group-directory API calls. |
| `OBOT_AUTHENTIK_AUTH_PROVIDER_API_TOKEN` | Yes | API token for the read-only service account above. |
| `OBOT_AUTHENTIK_AUTH_PROVIDER_GROUPS_CLAIM` | No (default `groups`) | ID token claim name carrying group names. |
| `OBOT_AUTH_PROVIDER_COOKIE_SECRET` | Yes | Random string of 16, 24, or 32 bytes, base64-encoded. Obot generates this automatically when you configure the provider through its UI. |
| `OBOT_AUTH_PROVIDER_EMAIL_DOMAINS` | Yes (default `*`) | Comma-separated allowed email domains, or `*` for any. |
| `OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION` | No (default `1h`) | How long a session is used before its token is refreshed. |
| `OBOT_AUTH_PROVIDER_ENABLE_LOGGING` | No | Set to `true` for oauth2-proxy request/auth logging. |
| `OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN` | No | Use a Postgres-backed session store instead of cookies. |
| `OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS` | No | Postgres session store pool size. |
| `OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS` | No | Postgres session store idle pool size. |
| `OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS` | No | Postgres session store connection lifetime. |

`OBOT_AUTH_PROVIDER_COOKIE_SECRET`, `OBOT_SERVER_URL` / `OBOT_SERVER_PUBLIC_URL`, and `PORT` are
set by Obot itself when it launches the provider process; they don't need to be configured
manually.
