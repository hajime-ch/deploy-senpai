# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /deploy-senpai ./cmd/deployer

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    docker-cli-compose \
    tzdata

# Create non-root user
RUN adduser -D -u 1000 deployer

# Create data directory
RUN mkdir -p /var/lib/deployer/deployments && \
    chown -R deployer:deployer /var/lib/deployer

# Copy binary
COPY --from=builder /deploy-senpai /usr/local/bin/deploy-senpai

# Note: Container needs access to Docker socket
# Mount /var/run/docker.sock when running

USER deployer

WORKDIR /app

ENTRYPOINT ["deploy-senpai"]
CMD ["serve", "--config", "/app/config.yaml"]
