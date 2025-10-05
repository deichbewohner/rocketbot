# rocketbot

**Experimental Rocket.Chat bot with streaming message support. Unmaintained.**

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
# Enable in .env
API_ADDR=:8080
BOT1_API_TOKEN=secret123

# Send message directly (no generator)
curl -X POST http://localhost:8080/api/v1/bots/BOT1/send \
  -H "Authorization: Bearer secret123" \
  -H "Content-Type: application/json" \
  -d '{"target":{"username":"alice"},"text":"Hello"}'
```

Target must specify exactly one of:
- `{"username": "alice"}` - Send DM to user
- `{"channel": "general"}` - Post to channel (# optional)
- `{"roomId": "ABC123"}` - Post to room ID

## Architecture

- **Generator Interface**: Implement `ResponseGenerator` for basic responses, add `GenerateResponseStream()` for streaming
- **1-Second Updates**: Chunks buffered and sent once/second via `chat.update` API
