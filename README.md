# aws-ecr-proxy

A minimal Go compatibility proxy that lets **VS Code Container Tools** browse
an AWS ECR registry the same way it browses Docker Hub or GHCR.

## Why

VS Code Container Tools expects a standard Docker Registry API, including
`GET /v2/_catalog`. AWS ECR does not implement `_catalog` and returns
`405 Method Not Allowed`. This proxy adds that missing endpoint, implemented
on top of the ECR `DescribeRepositories` API.

It also works around a few other gaps between ECR's Docker Registry API and
what a generic client like VS Code expects:

- VS Code has no ECR credential helper wired up for a `localhost` registry,
  so it can never obtain a valid ECR token itself. The proxy fetches its own
  token via `ecr:GetAuthorizationToken` (using the AWS credential chain) and
  injects it into every forwarded request, so no client-side ECR auth is
  needed at all.
- ECR paginates `tags/list` for repositories with many tags using a `Link:
  rel="next"` header. Some HTTP clients (VS Code's Node-based registry
  client included) do not correctly re-encode that header's cursor when
  following it, which ECR then rejects. The proxy fetches and aggregates
  every page itself, server side, and returns the full tag list in one
  response, so the client never has to follow that header at all.

```
VS Code -> Container Tools -> aws-ecr-proxy -> ECR (DescribeRepositories, GetAuthorizationToken, Docker Registry API)
```

This is **not** a full Docker Registry implementation. It only supports
read-only browsing:

- `GET /v2/`, registry recognition check
- `GET /v2/_catalog`, repository list, backed by ECR `DescribeRepositories`
- `GET /v2/<repository>/tags/list`, full tag list, aggregated from ECR's
  paginated response
- `GET /health`, container health check
- Everything else under `/v2/` (manifests, blobs) is forwarded as-is to ECR

Push, delete, repository management, and lifecycle policies are out of
scope; ECR remains the source of truth for everything except the catalog
listing and tag aggregation.

## Configuration

Set via environment variables (see [.env.example](.env.example)):

| Variable                | Required | Default | Description                                  |
|--------------------------|----------|---------|-----------------------------------------------|
| `AWS_REGION`             | yes      | none    | AWS region of the ECR registry                |
| `ECR_REGISTRY`           | yes      | none    | ECR registry host, e.g. `123456789012.dkr.ecr.eu-central-1.amazonaws.com` |
| `PORT`                   | no       | `5000`  | Port the proxy listens on                     |
| `CATALOG_CACHE_SECONDS`  | no       | `60`    | How long `/v2/_catalog` and `/v2/<repo>/tags/list` results are cached |
| `AWS_PROFILE`            | no       | none    | AWS CLI profile to use instead of `[default]`  |

AWS credentials are resolved via the standard AWS SDK for Go v2 credential
chain (environment variables, `~/.aws/credentials`, `~/.aws/config`, AWS
profiles, or an IAM role when running in AWS). Credentials are never logged,
hardcoded, or embedded in the image.

If you use AWS SSO, set `AWS_PROFILE` to the SSO profile that has access to
the ECR registry. The `docker-compose.yml` mount for `~/.aws` is read-write
on purpose: the SDK needs to write refreshed SSO tokens back to
`~/.aws/sso/cache`, and a read-only mount silently breaks that refresh,
forcing a full manual re-login far more often than necessary.

## Running locally

```bash
cp .env.example .env
# edit .env with your AWS_REGION and ECR_REGISTRY
docker compose up --build
```

### Manual testing

```bash
# Health check
curl http://localhost:5000/health

# Registry recognition check
curl http://localhost:5000/v2/

# Repository catalog (backed by ECR DescribeRepositories)
curl http://localhost:5000/v2/_catalog
```

Expected catalog response:

```json
{
  "repositories": [
    "backend",
    "frontend",
    "worker"
  ]
}
```

```bash
# Tags for a specific repository (aggregated across all of ECR's pages)
curl http://localhost:5000/v2/backend/tags/list
```

The tags response is aggregated by the proxy from all of ECR's paginated
pages and cached per repository (see `CATALOG_CACHE_SECONDS`), so the client
always gets the full tag list in a single request.

## Troubleshooting

### `401`, `UnrecognizedClientException`, or `Not Authorized`

Your AWS credentials are missing, unreadable, or expired. Check
`docker compose logs ecr-vscode-proxy` for the underlying error. If you use
AWS SSO and see something like `failed to refresh cached credentials` or
`read-only file system`, re-authenticate with:

```bash
aws sso login --profile <your-profile>
```

### `405 Method Not Allowed` on `/v2/_catalog`

This is the exact problem this proxy exists to fix. If you still see it,
make sure you're pointing VS Code at the proxy (`localhost:5000`), not at
the real ECR registry host directly.

## Running tests

```bash
go test ./...
```

Or, using the Makefile:

```bash
make test   # go test ./...
make build  # build the binary into bin/
make vet    # go vet ./...
make fmt    # gofmt -l .
```

## VS Code Container Tools setup

1. Start the proxy (`docker compose up --build`) so it's reachable at
   `http://localhost:5000` (or wherever you deploy it).
2. In VS Code, open the **Containers** view, go to the **Registries** panel,
   and add a new generic Docker Registry pointing at the proxy's address
   (e.g. `localhost:5000`).
3. The new registry entry will list your ECR repositories (via
   `/v2/_catalog`) and, once you drill into a repository, its tags (via
   `/v2/<repository>/tags/list`, aggregated from ECR).

Existing GitHub/Docker Hub registries in Container Tools are unaffected,
this proxy only acts as one additional registry entry.

## Project layout

```
cmd/server/main.go        - wiring: config, AWS SDK, catalog, tags cache, proxy, HTTP server
internal/config/          - environment variable configuration
internal/catalog/         - ECR DescribeRepositories -> cached, sorted catalog
internal/ecrauth/         - ECR GetAuthorizationToken, cached and injected into proxied requests
internal/tagscache/       - fetches and caches the full, paginated tags/list per repository
internal/proxy/           - reverse proxy to the configured ECR registry (manifests, blobs)
internal/server/          - HTTP routing (/health, /v2/, /v2/_catalog, /tags/list, proxy fallback)
```

## Security notes

- Never logs AWS credentials, ECR tokens, or `Authorization` headers.
- Only ever proxies to the single configured `ECR_REGISTRY` host, it is not
  a general-purpose HTTP proxy and cannot be redirected elsewhere.
- Runs as a non-root user in the container image.
- ECR errors are logged server-side but returned to clients as a generic
  Docker Registry-style error, without internal details.
- The client's own `Authorization` header is always replaced with the
  proxy's own ECR token before forwarding, it is never trusted or forwarded
  upstream as-is.
