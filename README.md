# Deploy Senpai

A lightweight service that automatically deploys feature branches to isolated staging environments. Push to any branch, get a running environment. You provide the docker-compose templates — the deployer handles the lifecycle.

## What It Does

- Receives GitHub webhooks on push/delete events
- Pulls the container image from your registry (ghcr.io by default)
- Renders your docker-compose template with branch-specific variables
- Runs `docker compose up` to start the environment
- Generates and persists passwords across re-deploys via `{{password "name"}}` template function
- Cleans up stale deployments automatically (by schedule or on branch deletion)
- Exposes a REST API for manual deploys, status checks, and management
- Provides Prometheus metrics and a health check endpoint

## What It Does NOT Do

- **No routing/proxy management.** There is no built-in Caddy, Traefik, or nginx integration. You handle routing yourself — either in your compose template (e.g. Traefik labels) or with an external reverse proxy.
- **No database-specific logic.** The deployer doesn't know or care what services you run. Postgres, MySQL, Redis, Kafka — it's all in your template.
- **No image building.** The deployer only pulls pre-built images. Build and push images in CI (e.g. GitHub Actions) before the webhook fires.
- **No multi-host orchestration.** It runs on a single Docker host. Not a replacement for Kubernetes.

## Quick Start

### Prerequisites

- Go 1.22+ (for building from source)
- Docker & Docker Compose v2
- GitHub repository with container images published to ghcr.io
- A reverse proxy of your choice for TLS and subdomain routing

### Installation

```bash
git clone https://github.com/hajime-ch/deploy-senpai.git
cd deploy-senpai
make build
```

Or with Docker:
```bash
docker compose up -d
```

### Setup

1. Create a compose template for your app (see [Templates](#templates) below)
2. Copy and edit the config:
   ```bash
   cp config.minimal.yaml config.yaml
   # Edit config.yaml — set your domain, GitHub token, app definitions
   ```
3. Set environment variables:
   ```bash
   export WEBHOOK_SECRET=$(openssl rand -hex 32)
   export GITHUB_TOKEN=your-token-here        # needs read:packages scope
   export DEPLOYER_ENCRYPTION_KEY=$(openssl rand -c 32)  # optional, for password encryption at rest
   ```
4. Run the deployer:
   ```bash
   ./deploy-senpai serve --config config.yaml
   ```
5. Configure the GitHub webhook (see [GitHub Webhook Setup](#github-webhook-setup))

## Configuration

The deployer reads a single YAML config file. Environment variables are expanded using `${VAR_NAME}` syntax.

### Minimal Config

```yaml
server:
  port: 8080
  webhook_secret: "${WEBHOOK_SECRET}"

domain:
  base_domain: "staging.example.com"

github:
  token: "${GITHUB_TOKEN}"
  owner: "your-github-org"

docker:
  network: "web"
  registry: "ghcr.io"

apps:
  api:
    repo: "my-api"
    compose_template: "./templates/app-with-db.yml.tpl"
    env:
      NODE_ENV: "staging"

  frontend:
    repo: "my-frontend"
    compose_template: "./templates/static-site.yml.tpl"

cleanup:
  on_branch_delete: true
```

### Full Config Reference

| Section | Field | Default | Description |
|---------|-------|---------|-------------|
| `server.host` | string | `0.0.0.0` | Listen address |
| `server.port` | int | `8080` | Listen port |
| `server.webhook_secret` | string | *required* | GitHub webhook HMAC secret. If unset, every webhook is rejected — the endpoint fails closed rather than accepting unsigned requests. |
| `server.enable_auth` | bool | `true` | Require API key for `/api/v1/*` endpoints |
| `server.api_keys` | list | — | Accepted API keys (when auth enabled) |
| `domain.base_domain` | string | *required** | Base domain for deployment URLs |
| `github.token` | string | *required* | GitHub PAT (needs `read:packages`) |
| `github.owner` | string | *required* | GitHub org or username |
| `docker.network` | string | `web` | Docker network for containers |
| `docker.registry` | string | `ghcr.io` | Container registry |
| `storage.data_dir` | string | `/var/lib/deployer` | Directory for deployment state |
| `defaults.cleanup_after_hours` | int | `168` (7 days) | Inactivity threshold for cleanup |
| `cleanup.schedule` | cron | `0 * * * *` | Cleanup check interval |
| `cleanup.on_branch_delete` | bool | `false` | Remove deployment when branch is deleted |
| `cleanup.keep_recent` | int | `3` | Min recent deployments to keep per app |
| `logging.level` | string | `info` | Log level (debug/info/warn/error) |
| `logging.format` | string | `json` | Log format (json/text) |
| `rate_limit.enabled` | bool | `false` | Enable rate limiting |
| `rate_limit.requests_per_minute` | int | `60` | Rate limit threshold |

\* `domain.base_domain` is only required when at least one app does not set its own `base_domain`. If every app defines a per-app `base_domain`, the global setting can be omitted.

### App Config

Each entry under `apps:` defines a deployable application:

```yaml
apps:
  my-app:
    repo: "my-app"                                    # GitHub repo name (required)
    image: "my-app"                                   # Image name (defaults to repo)
    compose_template: "./templates/app-with-db.yml.tpl"  # Path to template (required)
    init_files: "./init-files/my-app/"                # Directory of files to copy into deploy dir (optional)
    deploy_ref: ""                                    # Fixed deployment slot name (optional, see Production Deployments)
    base_domain: ""                                   # Override global domain.base_domain for this app (optional)
    env:                                              # Environment variables passed to template (optional)
      NODE_ENV: "staging"
      LOG_LEVEL: "debug"
    scripts:                                          # Lifecycle scripts (optional, see Lifecycle Scripts)
      pre_deploy: "./scripts/my-app/pre-deploy.sh"
      post_deploy: "./scripts/my-app/post-deploy.sh"
```

- **`compose_template`** (required) — path to a Go template file that produces a `docker-compose.yml`. Must exist on disk at startup.
- **`init_files`** (optional) — path to a directory. All files in it are copied into the deployment directory before `docker compose up`. Useful for SQL seed files, config files, etc. that your compose template mounts as volumes.

## Lifecycle Scripts

You can run custom scripts at key points in the deployment lifecycle — for notifications (Slack, Discord, PagerDuty), DNS record management, cache warming, or anything else.

Configure scripts per app:

```yaml
apps:
  my-app:
    repo: "my-app"
    compose_template: "./templates/app.yml.tpl"
    scripts:
      pre_deploy: "./scripts/my-app/pre-deploy.sh"
      post_deploy: "./scripts/my-app/post-deploy.sh"
      pre_remove: "./scripts/my-app/pre-remove.sh"
      post_remove: "./scripts/my-app/post-remove.sh"
```

### Execution Order

| Hook | When it runs | On failure |
|------|-------------|------------|
| `pre_deploy` | After init files copy, before template rendering | Aborts deploy (marks failed) |
| `post_deploy` | After deployment is running | Logs warning, does NOT roll back |
| `pre_remove` | Before `docker compose down` | Aborts removal |
| `post_remove` | After state cleanup | Logs warning only |

### Environment Variables

Scripts receive deployment context as environment variables:

| Variable | Description |
|----------|-------------|
| `DEPLOY_APP` | App name |
| `DEPLOY_BRANCH` | Original branch name |
| `DEPLOY_SANITIZED_BRANCH` | DNS-safe branch name |
| `DEPLOY_URL` | Deployment URL |
| `DEPLOY_IMAGE_TAG` | Image tag (commit SHA or tag name) |
| `DEPLOY_ID` | Unique deployment ID |
| `DEPLOY_STATUS` | Current deployment status |

Each script has a **60-second timeout**. All script paths are validated at startup — the deployer refuses to start if a configured script doesn't exist on disk.

## Templates

Templates are standard Go `text/template` files. The deployer renders them into `docker-compose.yml` files for each deployment.

### Available Variables

| Variable | Example | Description |
|----------|---------|-------------|
| `{{.AppName}}` | `my-app` | App name from config |
| `{{.Branch}}` | `feature/login` | Original branch name |
| `{{.SanitizedBranch}}` | `feature-login` | DNS-safe branch name |
| `{{.ImageTag}}` | `abc1234` | Short commit SHA (or branch name) |
| `{{.Image}}` | `ghcr.io/org/my-app:abc1234` | Full image reference |
| `{{.Registry}}` | `ghcr.io` | Container registry |
| `{{.Owner}}` | `your-org` | GitHub owner |
| `{{.BaseDomain}}` | `staging.example.com` | Base domain from config |
| `{{.Subdomain}}` | `my-app-feature-login` | Computed subdomain |
| `{{.Network}}` | `web` | Docker network from config |
| `{{.Env}}` | map | Environment variables from app config |

Branch and tag names are validated before rendering — they must match
`[A-Za-z0-9._/-]`, be at most 255 characters, and contain no `..` — so
`{{.Branch}}` cannot inject YAML into the generated compose file. Use
`{{.SanitizedBranch}}` anywhere the value becomes a DNS name or container name.

This is stricter than git, which also permits `#`, `+`, `&`, `{`, `}` and
unicode in branch names. A branch like `feature/fix-#123` is rejected with
`invalid branch name` rather than deployed. If that blocks your team, the
character set and the rules for widening it safely are documented at
`refNamePattern` in `internal/config/config.go`.

### The `password` Function

Use `{{password "name"}}` to generate a random 32-character hex password. The same name always returns the same password for a given deployment — passwords are persisted in encrypted metadata and reused across re-deploys.

```yaml
environment:
  POSTGRES_PASSWORD: "{{password "dbpass"}}"
  REDIS_PASSWORD: "{{password "redis"}}"
```

You can use the same name in multiple places (e.g. in both the app and database service) to share a password.

### Example: App + PostgreSQL

```yaml
services:
  app:
    image: {{.Image}}
    container_name: {{.AppName}}-{{.SanitizedBranch}}-app
    restart: unless-stopped
    networks:
      - {{.Network}}
      - internal
    environment:
      DATABASE_URL: "postgres://app:{{password "dbpass"}}@{{.AppName}}-{{.SanitizedBranch}}-db:5432/app?sslmode=disable"
{{- range $key, $value := .Env}}
      {{$key}}: "{{$value}}"
{{- end}}
    depends_on:
      db:
        condition: service_healthy

  db:
    image: postgres:16-alpine
    container_name: {{.AppName}}-{{.SanitizedBranch}}-db
    restart: unless-stopped
    networks:
      - internal
    environment:
      POSTGRES_USER: "app"
      POSTGRES_PASSWORD: "{{password "dbpass"}}"
      POSTGRES_DB: "app"
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app -d app"]
      interval: 5s
      timeout: 5s
      retries: 10

networks:
  {{.Network}}:
    external: true
  internal:
    driver: bridge

volumes:
  db-data:
```

### Example: Static Site

```yaml
services:
  app:
    image: {{.Image}}
    container_name: {{.AppName}}-{{.SanitizedBranch}}-app
    restart: unless-stopped
    networks:
      - {{.Network}}
    environment:
      BASE_URL: "https://{{.Subdomain}}.{{.BaseDomain}}"
{{- range $key, $value := .Env}}
      {{$key}}: "{{$value}}"
{{- end}}

networks:
  {{.Network}}:
    external: true
```

More examples are in the `templates/` directory.

## CLI Usage

The `deploy-senpai` binary doubles as a CLI client that talks to a running server. Configure the server URL and API key via flags or environment variables:

```bash
# Via environment variables (recommended)
export DEPLOY_SENPAI_URL=https://deployer.example.com:8080
export DEPLOY_SENPAI_API_KEY=your-key

# Or via flags
deploy-senpai --server https://deployer:8080 --api-key your-key list
```

### Available Commands

| Command | Description |
|---------|-------------|
| `deploy-senpai serve` | Start the server |
| `deploy-senpai list` | List all deployments |
| `deploy-senpai deploy <app> <branch>` | Deploy an app branch |
| `deploy-senpai status <app> <branch>` | Show deployment status |
| `deploy-senpai remove <app> <branch>` | Remove a deployment |
| `deploy-senpai apps` | List configured apps |
| `deploy-senpai cleanup` | Trigger cleanup of stale deployments |
| `deploy-senpai health` | Check server health |
| `deploy-senpai version` | Print version |

### Examples

```bash
# List all deployments
deploy-senpai list

# Deploy with a specific image tag and wait for it to be running
deploy-senpai deploy my-app feature-login --image-tag abc1234 --wait

# Get deployment status as JSON
deploy-senpai status my-app feature-login --json

# Remove a deployment
deploy-senpai remove my-app feature-login

# Check server health
deploy-senpai health
```

All client commands support `--json` for machine-readable output.

## How It Works

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│   GitHub     │────>│   Deployer   │────>│  Docker Engine   │
│   Webhook    │     │   Service    │     │                  │
└─────────────┘     └──────────────┘     └─────────────────┘
                           │
                           ▼
                    ┌──────────────┐
                    │  Your Compose │
                    │  Template     │
                    └──────────────┘
```

1. Developer pushes to `feature/login`
2. CI builds and pushes image `ghcr.io/org/app:abc1234`
3. GitHub sends webhook to the deployer
4. Deployer renders your compose template with branch-specific variables
5. Deployer pulls the image and runs `docker compose up -d --remove-orphans`
6. Environment is available at `my-app-feature-login.staging.example.com` (assuming you've configured your reverse proxy)

### URL Format

Branch names are converted to DNS-safe subdomains:

| Branch | Subdomain |
|--------|-----------|
| `feature/login` | `my-app-feature-login` |
| `bugfix/fix-auth` | `my-app-bugfix-fix-auth` |
| `ISSUE-123` | `my-app-issue-123` |
| `release.1.0` | `my-app-release-1-0` |

Rules: lowercased, `/_.` replaced with `-`, non-alphanumeric chars removed, truncated to 63 characters (DNS label limit).

## GitHub Webhook Setup

1. Go to your repository Settings > Webhooks > Add webhook
2. **Payload URL:** `https://your-deployer-host:8080/webhook/github`
3. **Content type:** `application/json`
4. **Secret:** Same value as `server.webhook_secret` in your config
5. **Events:** Select "Pushes" and "Branch or tag deletion"

## API Reference

All `/api/v1/*` endpoints require authentication when `server.enable_auth` is true. Authenticate with:
- `X-API-Key: your-key` header (recommended)
- `Authorization: Bearer your-key` header
- `?api_key=your-key` query parameter

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/health` | No | Health check (Docker, storage) |
| `GET` | `/metrics` | No | Prometheus metrics |
| `POST` | `/webhook/github` | Webhook secret | GitHub webhook receiver |
| `GET` | `/api/v1/deployments` | Yes | List all deployments |
| `GET` | `/api/v1/deployments/status/{id}` | Yes | Get deployment by ID |
| `GET` | `/api/v1/deployments/{app}/{branch}` | Yes | Get specific deployment |
| `POST` | `/api/v1/deployments/{app}/{branch}` | Yes | Trigger manual deploy (async: `202` + deployment id) |
| `DELETE` | `/api/v1/deployments/{app}/{branch}` | Yes | Remove deployment |
| `POST` | `/api/v1/cleanup` | Yes | Trigger cleanup |
| `GET` | `/api/v1/apps` | Yes | List configured apps |

### Manual Deploy

```bash
curl -X POST https://deployer:8080/api/v1/deployments/my-app/feature-login \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"image_tag": "abc1234"}'
```

If `image_tag` is omitted, the sanitized branch name is used.

The call is asynchronous: it answers `202 Accepted` with the deployment record as
soon as the deploy is queued, and the deploy (registry login, image pull, compose
up) continues in the background. Poll `GET /api/v1/deployments/status/{id}` with
the `id` from the response for the outcome — `status` moves through `pending` →
`in_progress` → `running` or `failed`, with `error_message` set on failure.

Do not expect the POST to block until the deploy finishes: a deploy routinely
takes longer than the server's write timeout and any reverse proxy in front of
it, which would turn a *successful* deploy into a dropped connection (typically
a `502`) for the caller.

Poll by ID rather than by `{app}/{branch}`: for apps with `deploy_ref` set, the
record is filed under the `deploy_ref` slot, so the app/branch lookup misses it.

## GitHub Actions Integration

Build and push your image in CI — the webhook triggers the deployer automatically:

```yaml
# .github/workflows/deploy-staging.yml
name: Deploy to Staging

on:
  push:
    branches-ignore:
      - main
      - master

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v5
        with:
          push: true
          tags: ghcr.io/${{ github.repository }}:${{ github.sha }}
```

The deployer uses the short SHA from the webhook as the image tag by default.

## Production / Tagged Deployments

The deployer can also handle production-style deployments using a fixed deployment slot. Instead of creating a new environment per branch, all deploys go to a single slot (e.g. `production`).

### How It Works

1. Define an app with `deploy_ref` in your config:
   ```yaml
   apps:
     my-app-prod:
       repo: "my-app"
       image: "my-app"
       compose_template: "./templates/production.yml.tpl"
       deploy_ref: "production"
       env:
         NODE_ENV: "production"
   ```

2. Every deploy to this app — regardless of branch or tag name — lands in the same slot: `my-app-prod/production`. The subdomain is always `my-app-prod-production.staging.example.com`.

3. Deployments to `deploy_ref` apps are **never auto-cleaned** by the cleanup scheduler.

### Triggering from CI (Recommended)

The simplest approach is to `curl` the API from your CI pipeline after building and pushing the image:

```bash
# In your CI pipeline (e.g. GitHub Actions, GitLab CI)
curl -X POST https://deployer:8080/api/v1/deployments/my-app-prod/production \
  -H "X-API-Key: $DEPLOYER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"image_tag": "v1.2.3"}'
```

The `{branch}` path parameter (`production`) is used as the deployment slot name. The `image_tag` in the body specifies which image to pull.

That call returns `202` as soon as the deploy is queued — it does not wait for
the containers to come up. To gate the pipeline on the result, poll
`GET /api/v1/deployments/status/{id}` until `status` is `running` or `failed`, as
in the example below.

### GitHub Actions Example

```yaml
# .github/workflows/deploy-production.yml
name: Deploy Production

on:
  push:
    tags:
      - 'v*'

jobs:
  build-and-deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v5
        with:
          push: true
          tags: ghcr.io/${{ github.repository }}:${{ github.ref_name }}

      - name: Deploy
        env:
          DEPLOYER_URL: ${{ vars.DEPLOYER_URL }}
          DEPLOYER_API_KEY: ${{ secrets.DEPLOYER_API_KEY }}
          IMAGE_TAG: ${{ github.ref_name }}
        run: |
          set -euo pipefail
          base="${DEPLOYER_URL%/}"

          # Trigger: answers 202 immediately with the deployment record.
          id=$(curl -fsS -X POST "$base/api/v1/deployments/my-app-prod/production" \
            -H "X-API-Key: $DEPLOYER_API_KEY" \
            -H "Content-Type: application/json" \
            -d "{\"image_tag\": \"$IMAGE_TAG\"}" | jq -r .id)
          echo "Triggered deployment $id"

          # Poll until the deploy settles.
          deadline=$(( SECONDS + 600 ))
          status=unknown
          while [ "$SECONDS" -lt "$deadline" ]; do
            sleep 5
            dep=$(curl -fsS --max-time 15 "$base/api/v1/deployments/status/$id" \
              -H "X-API-Key: $DEPLOYER_API_KEY") || { echo "  poll failed, retrying"; continue; }
            status=$(jq -r .status <<<"$dep")
            case "$status" in
              running)
                echo "Deployed: $(jq -r .url <<<"$dep")"
                exit 0 ;;
              failed)
                echo "::error::Deployment failed: $(jq -r .error_message <<<"$dep")"
                exit 1 ;;
              *)
                echo "  status: $status" ;;
            esac
          done

          echo "::error::Timed out waiting for deployment $id (last status: $status)"
          exit 1
```

### Tag Push Webhooks

If you've configured the GitHub webhook for tag events, tag pushes are also handled automatically. When a tag like `v1.2.3` is pushed:

1. The webhook parser extracts the tag name (`v1.2.3`) and uses it as both the branch name and the image tag
2. If the app has `deploy_ref` set, the deployment goes to the fixed slot
3. The full tag name (e.g. `v1.2.3`) is used as the image tag — not a short SHA

This means you can push a tag and have it auto-deploy, as long as the image was already built and pushed to the registry (e.g. by your CI pipeline).

## Cleanup

Deployments are cleaned up when:

1. **Branch deleted** — if `cleanup.on_branch_delete: true`, removed immediately on branch deletion webhook
2. **Inactivity** — deployments older than `cleanup_after_hours` are removed on the cleanup schedule
3. **Manual** — via `DELETE /api/v1/deployments/{app}/{branch}` or `POST /api/v1/cleanup`

Protected branches (`main`, `master`, `develop`, `staging`, `production`) are never auto-cleaned.

## Deployment State

Each deployment has a status: `pending` > `in_progress` > `running` or `failed`.

State is persisted to disk at `/var/lib/deployer/deployments/{app}/{branch}/metadata.json`. Passwords generated by `{{password}}` are encrypted with AES-256-GCM when `DEPLOYER_ENCRYPTION_KEY` is set (recommended for production).

The deployer restores deployment state on restart by scanning for existing `docker-compose.yml` files and metadata.

## Security

See [docs/SECURITY.md](docs/SECURITY.md) for full details. Key points:

- **Webhook validation** — HMAC-SHA256 via `X-Hub-Signature-256`
- **API authentication** — API key required for management endpoints
- **Plaintext token detection** — refuses to start if GitHub token is hardcoded (must use `${ENV_VAR}` syntax)
- **Rate limiting** — configurable token bucket rate limiter
- **Security headers** — `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, etc.
- **Password encryption at rest** — AES-256-GCM when `DEPLOYER_ENCRYPTION_KEY` is set
- **Systemd hardening** — service file with `NoNewPrivileges`, `ProtectSystem=strict`, restricted syscalls

## Troubleshooting

### Deployment not starting
```bash
# Check deployer logs
journalctl -u deploy-senpai -f

# Check if the image exists in the registry
docker pull ghcr.io/your-org/your-app:abc1234
```

### Containers not running
```bash
# Check compose output
docker compose -f /var/lib/deployer/deployments/my-app/feature-login/docker-compose.yml ps
docker compose -f /var/lib/deployer/deployments/my-app/feature-login/docker-compose.yml logs
```

### Webhook not triggering
```bash
# Check the health endpoint
curl http://localhost:8080/health

# Check GitHub webhook deliveries in repo Settings > Webhooks > Recent Deliveries
```

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.

## Security

If you discover a security vulnerability, please report it responsibly by emailing security@hajime.ch. See [docs/SECURITY.md](docs/SECURITY.md) for full details.
