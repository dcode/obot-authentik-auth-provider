# Installation

Obot loads auth providers from **provider registry directories**: each is a plain directory
containing an `auth-providers/<name>.yaml` manifest next to a `<name>/bin/obot-provider` binary.
Obot's `ProviderRegistries` config (a comma-separated list of local filesystem paths) tells it
which directories to scan; this is how the built-in Google/Okta/Entra providers are loaded too --
they're baked into the same paths inside Obot's own container image.

Every [release](https://github.com/dcode/obot-authentik-auth-provider/releases) archive already
has that exact layout:

```text
auth-providers/authentik-auth-provider.yaml
authentik-auth-provider/bin/obot-provider
```

so extracting one into a directory Obot can see is the entire installation step.

## Kubernetes (init container)

The simplest way to get a release archive onto Obot's pod without rebuilding its image is an
init container that downloads and extracts it into a volume shared with the main container:

```yaml
initContainers:
  - name: authentik-auth-provider
    image: alpine:3
    command:
      - sh
      - -c
      - |
        wget -qO- \
          https://github.com/dcode/obot-authentik-auth-provider/releases/download/vX.Y.Z/authentik-auth-provider_X.Y.Z_linux_amd64.tar.gz \
          | tar -xzf - -C /providers
    volumeMounts:
      - name: authentik-provider
        mountPath: /providers
volumes:
  - name: authentik-provider
    emptyDir: {}
```

Mount the same `authentik-provider` volume into Obot's main container (e.g. at
`/extra-providers`), then point Obot at it via `--provider-registries`.

**This has to be a literal CLI arg on Obot's own container (`server
--provider-registries=/obot-providers/enterprise-providers,/obot-providers/providers,/extra-providers`),
not the `OBOT_SERVER_PROVIDER_REGISTRIES` environment variable** documented in `obot server
--help` -- confirmed live against a real deployment that the env var is inert (the startup log's
"Loaded provider registry" line is byte-for-byte identical whether it's set or not).
`--provider-registries` is a pflag string-slice flag, and this Obot codebase's
cobra/viper env-var binding apparently doesn't cover that flag type, only scalars.

Also note that flag **replaces** Obot's default registry list rather than appending to it, so you
must re-list the built-in paths (`/obot-providers/enterprise-providers`,
`/obot-providers/providers` -- read these off your own pod's startup log rather than trusting
this exact string, in case a future Obot version changes them) alongside `/extra-providers`, or
every other provider (Google, GitHub, etc.) silently disappears from Obot's Auth Providers list.
If your chart has no `args`/`extraArgs` value to set this through, a Kustomize `postRenderers`
patch on the Deployment (or an equivalent tool for your deployment mechanism) is one way to add
the CLI arg without forking the chart -- see this provider's own consumer,
[dcode/infra's `helmrelease.yaml`](https://github.com/dcode/infra/blob/main/kubernetes/apps/obot/base/helmrelease.yaml),
for a worked example.

Pin the release version and architecture to match your node pool -- swap `linux_amd64` for
`linux_arm64` on arm64 nodes.

## Custom image

Alternatively, build a derivative of Obot's own image that layers the archive contents on top,
the same way Obot's own `Dockerfile` copies in `obot-platform/providers` and
`obot-platform/enterprise-providers`:

```dockerfile
FROM ghcr.io/obot-platform/obot:vX.Y.Z AS obot

FROM alpine:3 AS authentik-provider
ADD https://github.com/dcode/obot-authentik-auth-provider/releases/download/vX.Y.Z/authentik-auth-provider_X.Y.Z_linux_amd64.tar.gz /tmp/provider.tar.gz
RUN mkdir -p /extra-providers && tar -xzf /tmp/provider.tar.gz -C /extra-providers

FROM obot
COPY --from=authentik-provider /extra-providers /extra-providers
```

Then set `--provider-registries` to include `/extra-providers`, alongside the built-in registry
paths -- see the CLI-arg-vs-env-var and replace-vs-append caveats above, both apply here too.

## After installing

Once the binary and manifest are in a registered directory, "Authentik" appears in Obot's admin
UI under **Auth Providers**. Configure it with the values from [Configuration](configuration.md)
before enabling it -- Obot only allows one auth provider configured at a time.
