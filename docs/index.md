# obot-authentik-auth-provider

An [Obot MCP Gateway](https://github.com/obot-platform/obot) auth provider that logs users in
through [Authentik](https://goauthentik.io/) via OIDC, and lets Obot's access-control UI browse
and match against Authentik groups.

## Why this exists

Obot Community ships Local, GitHub, and Google auth providers for free. Entra, Okta, Auth0, and
JumpCloud are also open source (MIT-licensed, built from the public
[`obot-platform/enterprise-providers`](https://github.com/obot-platform/enterprise-providers) repo)
but are gated behind a paid Keygen license entitlement (`OBOT_ENTERPRISE_AUTH_PROVIDERS`), enforced
by Obot's own control plane at both configure-time and on every OAuth/MCP-connect request.
Authentik isn't in that list at all, licensed or not.

Obot's auth providers, however, are not baked into the core application. Each one is a small,
standalone process -- a thin wrapper around a forked `oauth2-proxy` -- discovered from a
filesystem directory (`ProviderRegistries` in Obot's config) that holds a YAML manifest next to a
compiled binary. The manifest is where the license gate actually lives: it is a single
self-declared `requiredEntitlements` field, present on Okta's manifest and absent from Google's.
A provider you build yourself simply never has that field, and is never license-checked.

This repo is that: a from-scratch provider following the same documented contract
(`obot-platform/providers`'s `docs/auth-providers.md`) and reusing the same shared libraries the
official providers do
([`auth-providers-common`](https://github.com/obot-platform/providers/tree/main/auth-providers-common),
[`authcommon`](https://github.com/obot-platform/enterprise-providers/tree/main/authcommon)) for
cursor-paginated group listing, concurrent group-ID resolution, and session-state handling.

Unlike Okta -- which needs a second "API Services" application and a Management API service
account just to read group membership, because Okta doesn't put groups in the ID token by
default -- Authentik can emit a `groups` claim directly, so the login flow itself never needs
anything beyond a standard OIDC client ID/secret. A separate, narrowly-scoped Authentik API token
is only used for the group-*directory* endpoints (browsing/searching all groups, and resolving a
user's memberships by ID) that back Obot's access-control UI.

## What's included

- OIDC login via Authentik, with group membership carried in the ID token
- A `groups` claim name that's configurable, so an existing custom claim (e.g. one already named
  `k8s_groups` for another application) can be reused instead of adding a new mapping
- Full group-directory support: paginated group listing/search, group-ID resolution, and
  per-user group lookups, all backing Obot's access-control UI the same way Okta/Entra do
- Cross-platform release binaries for every OS/arch combination Obot itself ships
  (`linux`/`darwin`/`windows` × `amd64`/`arm64`)

See [Installation](installation.md) for how to wire a release archive into an Obot deployment, and
[Configuration](configuration.md) for the full environment variable reference and the Authentik-side
setup (OAuth2 provider, scope mapping, API token) it expects.
