# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`update-cloudflare` is a Go application that periodically checks the host's external IP address and updates a Cloudflare DNS A record when the IP changes. It is designed for homelab environments with dynamic IPs. It ships as a Docker image and a Helm chart for Kubernetes deployment.

## Common Commands

```bash
# Build
go build -o update-cloudflare .

# Run tests
go test ./...

# Format code
go fmt ./...

# Vet code
go vet ./...

# Build Docker image
docker build -t update-cloudflare .

# Run locally (requires CF_API_TOKEN and env vars)
DNS_NAME=home.example.com ZONE_ID=<zone-id> CF_API_TOKEN=<token> ./update-cloudflare -console

# Lint Helm chart
helm lint charts/update-cloudflare/

# Template Helm chart (dry-run)
helm template update-cloudflare charts/update-cloudflare/ --set dnsName=home.example.com --set zoneId=<zone-id>
```

## Architecture

All application logic lives in `main.go` (single file):

1. **Configuration** — Reads CLI flags (`-console`, `-port`) and required environment variables (`DNS_NAME`, `ZONE_ID`, `DNS_TTL`, `CHECK_IP`, `SLEEP_PERIOD`). The Cloudflare API token is read from `CF_API_TOKEN` in `run()` after config parsing.

2. **HTTP server** — Runs on port 8080 (configurable) serving `/healthz` (health probe) and `/metrics` (Prometheus). The server runs in a goroutine alongside the main loop.

3. **Main loop** — Polls `checkip.amazonaws.com` (or custom URL) for the current external IP, compares it to the existing Cloudflare DNS record, and creates or updates the record if the IP or TTL has changed. TTL `1` means Cloudflare Auto TTL (the default). Tracks cumulative update duration as the Prometheus counter `update_cloudflare_duration_total`.

## Helm Chart

The Helm chart is at `charts/update-cloudflare/`. Key templating notes:

- Cloudflare credentials are supplied either through an existing secret (`secret.existingSecret`) or by letting the chart create one (`secret.create: true`). The secret must contain `CF_API_TOKEN`.
- Chart version and app version are in `Chart.yaml` and must be bumped manually on changes.
- The Helm release CI workflow (`helm-release.yml`) uses `chart-releaser` and publishes to `https://jpflouret.github.io/update-cloudflare/`.

## Commit Guidelines

- Make one commit per distinct change (e.g. separate commits for Go, Dockerfile, and Helm changes)
- Keep commit messages short
- Do not add co-authored-by tags

## CI/CD

Two GitHub Actions workflows:
- **`docker-image.yml`** — Builds and pushes multi-platform (`linux/amd64`, `linux/arm64`) images to Docker Hub (`jpflouret/update-cloudflare`) and GHCR on pushes to `main` and semver tags.
- **`helm-release.yml`** — Runs `chart-releaser` on pushes to `main` to publish updated Helm chart releases.
