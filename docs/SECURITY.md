# Security Documentation

This document describes the security features and configuration of the Deploy Senpai service.

## Overview

The Deploy Senpai has been hardened with multiple layers of security to protect against common attack vectors while maintaining functionality for legitimate use cases.

## Security Features

### 1. API Authentication

All `/api/v1/*` endpoints require authentication when `server.enable_auth` is enabled.

**Configuration:**

```yaml
server:
  enable_auth: true
  api_keys:
    - "${API_KEY_1}"
    - "${API_KEY_2}"
```

**Supported Authentication Methods:**

- `X-API-Key` header (recommended)
- `Authorization: Bearer <token>` header
- `api_key` query parameter (not recommended for production)

**Public Endpoints (no auth required):**

- `GET /health` - Health check
- `POST /webhook/github` - GitHub webhook (uses webhook secret)
- `GET /metrics` - Prometheus metrics

### 2. Rate Limiting

Token bucket rate limiting protects against DoS attacks and API abuse.

**Configuration:**

```yaml
rate_limit:
  enabled: true
  requests_per_minute: 60
```

**Response Headers:**

- `X-RateLimit-Limit` - Maximum requests per minute
- `X-RateLimit-Remaining` - Remaining requests in current window
- `Retry-After` - Seconds until next request allowed (when rate limited)

**Rate limited requests receive HTTP 429 Too Many Requests.**

### 3. Security Headers

All responses include security headers:

| Header                    | Value                                        | Purpose               |
| ------------------------- | -------------------------------------------- | --------------------- |
| `X-Content-Type-Options`  | `nosniff`                                    | Prevent MIME sniffing |
| `X-Frame-Options`         | `DENY`                                       | Prevent clickjacking  |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` | Strict CSP            |
| `Referrer-Policy`         | `strict-origin-when-cross-origin`            | Control referrer      |
| `Cache-Control`           | `no-store, no-cache, must-revalidate`        | Disable caching       |

### 4. Secrets Management

**Environment Variables:**
Sensitive configuration should use environment variable syntax:

```yaml
github:
  token: "${GITHUB_TOKEN}"
server:
  webhook_secret: "${WEBHOOK_SECRET}"
  api_keys:
    - "${API_KEY_1}"
```

**Plaintext Token Detection:**
The application will refuse to start if it detects plaintext GitHub tokens (prefixes: `ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`).

**Password Encryption:**
Passwords generated via `{{password "name"}}` in compose templates can be encrypted at rest when `DEPLOYER_ENCRYPTION_KEY` is set (32-byte key for AES-256-GCM).

### 5. Systemd Hardening

The systemd service file includes extensive security restrictions:

| Setting                                            | Purpose                                   |
| -------------------------------------------------- | ----------------------------------------- |
| `NoNewPrivileges=true`                             | Prevent privilege escalation              |
| `ProtectSystem=strict`                             | Read-only filesystem except allowed paths |
| `PrivateDevices=true`                              | No access to physical devices             |
| `ProtectKernelTunables=true`                       | No sysctl modifications                   |
| `ProtectKernelModules=true`                        | No kernel module loading                  |
| `RestrictNamespaces=true`                          | Limit namespace operations                |
| `MemoryDenyWriteExecute=true`                      | Prevent W^X violations                    |
| `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` | Only network sockets                      |
| `SystemCallFilter=@system-service`                 | Restrict system calls                     |

## Docker Socket Security

The deployer requires Docker access. Consider these options:

### Option 1: Docker Socket Proxy (Recommended)

Use [Tecnativa/docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy):

```yaml
services:
  docker-proxy:
    image: tecnativa/docker-socket-proxy
    environment:
      CONTAINERS: 1
      IMAGES: 1
      NETWORKS: 1
      VOLUMES: 1
      POST: 1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
```

Configure deployer to use proxy:

```yaml
docker:
  host: "tcp://docker-proxy:2375"
```

### Option 2: Rootless Docker

Run Docker in rootless mode to reduce attack surface:

```bash
dockerd-rootless-setuptool.sh install
```

### Option 3: Docker Group

Add the deployer user to the docker group (least secure):

```bash
usermod -aG docker deployer
```

## Webhook Security

GitHub webhooks are validated using HMAC-SHA256:

1. Configure a strong webhook secret (32+ characters)
2. Set the same secret in GitHub webhook settings
3. The deployer validates the `X-Hub-Signature-256` header

**Generate a secure secret:**

```bash
openssl rand -hex 32
```

## Network Security Recommendations

1. **Firewall Rules:**
   - Allow inbound: deployer port (8080 from internal only)
   - Allow outbound: 443 (GitHub, registry)

2. **TLS Configuration:**
   - Handle TLS termination with your reverse proxy of choice
   - Ensure proper certificate handling

3. **Network Isolation:**
   - Use Docker networks to isolate deployments
   - Consider network policies in Kubernetes

## Monitoring and Alerting

### Metrics Endpoint

Prometheus-compatible metrics at `/metrics`:

- `deployer_deployments_total` - Total deployment attempts
- `deployer_deployments_succeeded_total` - Successful deployments
- `deployer_deployments_failed_total` - Failed deployments
- `deployer_http_requests_total` - Total HTTP requests
- `deployer_webhooks_received_total` - Webhook events received

### Health Check

The `/health` endpoint returns:

- Component status (Docker, storage)
- Deployment count
- Overall health status (ok/degraded/unhealthy)

### Recommended Alerts

- High deployment failure rate
- Rate limiting triggered frequently
- Health check degraded
- Authentication failures spike

## Incident Response

### Suspected Compromise

1. Disable the deployer service: `systemctl stop deploy-senpai`
2. Revoke GitHub token and generate new one
3. Rotate all API keys
4. Review deployment logs
5. Check container images for tampering

### API Key Rotation

1. Add new key to config
2. Reload service
3. Update clients to use new key
4. Remove old key from config
5. Reload service

## Security Checklist

- [ ] Enable API authentication
- [ ] Use environment variables for secrets
- [ ] Enable rate limiting
- [ ] Use webhook secret
- [ ] Deploy with hardened systemd unit
- [ ] Consider Docker socket proxy
- [ ] Enable monitoring and alerting
- [ ] Regular security updates

## Reporting Security Issues

If you discover a security vulnerability, please report it responsibly by emailing security@hajime.ch. Do not create public issues for security vulnerabilities.
