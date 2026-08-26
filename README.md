# obot-authentik-auth-provider

An [Obot MCP Gateway](https://github.com/obot-platform/obot) auth provider for [Authentik](https://goauthentik.io/):
OIDC login plus a group-directory API backing Obot's access-control UI, following the same
provider contract Obot's own Google/Okta/Entra providers use -- no enterprise license required.

Full docs: <https://dcode.github.io/obot-authentik-auth-provider/>

## Development

```sh
go build ./...
go test -race ./...
pre-commit install
pre-commit run --all-files
```

Docs are built with [Zensical](https://github.com/zensical/zensical):

```sh
uvx zensical serve
```

## Releasing

Tagging a `vX.Y.Z` release triggers [GoReleaser](https://goreleaser.com/) to build binaries for
`linux`/`darwin`/`windows` × `amd64`/`arm64` and attach them to a GitHub Release. See
[docs/installation.md](docs/installation.md) for how a release archive gets into an Obot
deployment.
