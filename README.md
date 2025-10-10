<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/logo.webp">
  <source media="(prefers-color-scheme: light)" srcset=".github/assets/logo.webp">
  <img alt="rocketbot" src=".github/assets/logo.webp" width="400">
</picture>

**Experimental Rocket.Chat bot with streaming message support. Unmaintained.**

[![CI](https://github.com/deichbewohner/rocketbot/actions/workflows/ci.yml/badge.svg?branch=release)](https://github.com/deichbewohner/rocketbot/actions/workflows/ci.yml)
[![Release Workflow](https://github.com/deichbewohner/rocketbot/actions/workflows/release.yml/badge.svg)](https://github.com/deichbewohner/rocketbot/actions/workflows/release.yml)
[![Latest Release](https://img.shields.io/github/v/release/deichbewohner/rocketbot)](https://github.com/deichbewohner/rocketbot/releases)
[![GHCR](https://img.shields.io/badge/ghcr.io-deichbewohner%2Frocketbot-informational)](https://ghcr.io/deichbewohner/rocketbot)

[![CodeQL](https://github.com/deichbewohner/rocketbot/actions/workflows/github-code-scanning/codeql/badge.svg?branch=release)](https://github.com/deichbewohner/rocketbot/actions/workflows/github-code-scanning/codeql)
[![Coverage](https://codecov.io/gh/deichbewohner/rocketbot/branch/release/graph/badge.svg)](https://codecov.io/gh/deichbewohner/rocketbot)
[![Go Report Card](https://goreportcard.com/badge/github.com/deichbewohner/rocketbot)](https://goreportcard.com/report/github.com/deichbewohner/rocketbot)
[![Go Version](https://img.shields.io/github/go-mod/go-version/deichbewohner/rocketbot)](https://github.com/deichbewohner/rocketbot/blob/release/go.mod)
[![License](https://img.shields.io/github/license/deichbewohner/rocketbot)](https://github.com/deichbewohner/rocketbot/blob/release/LICENSE)

</div>

## Security

- Not internet-facing. Do not expose the HTTP API publicly.
- Bind `API_ADDR` to `127.0.0.1` or a private interface and place behind
  authenticated proxy.
- Limit egress to only your Rocket.Chat host and configured webhook endpoints
  using firewall or network policies (iptables/nftables, eBPF, or Kubernetes
  NetworkPolicy).
- See `SECURITY.md` for the pragmatic policy.

## Features

- WebSocket-based Rocket.Chat client
- Streaming response updates (1-second buffering)
- Pluggable response generators (streaming + non-streaming)
- Multi-bot support
- HTTP API for programmatic triggers

## Quick Start

```bash
# Copy config
cp .env.example .env

# Edit credentials
vim .env

# Run
( set -a; . .env; set +a; go run . )
```

## HTTP API

```bash
# Enable in .env (bind to loopback)
API_ADDR=127.0.0.1:8080
BOT1_SLUG=bob
BOT1_API_TOKEN=secret123

# Send message directly (no generator)
curl -X POST http://localhost:8080/api/v1/bots/bob/send \
  -H "Authorization: Bearer secret123" \
  -H "Content-Type: application/json" \
  -d '{"target":{"username":"alice"},"text":"Hello"}'
```

Target must specify exactly one of:
- `{"username": "alice"}` - Send DM to user
- `{"channel": "general"}` - Post to channel (# optional)
- `{"roomId": "ABC123"}` - Post to room ID
