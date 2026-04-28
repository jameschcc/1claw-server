# 1Claw Server — Go WebSocket Gateway

1Claw Server is the backend gateway for the 1Claw multi-agent platform. It manages multiple Hermes agent profiles simultaneously, routes WebSocket connections from mobile clients, and provides REST APIs for profile management.

## Architecture

```
Mobile Client (WS) <-> Go Gateway (this server) <-> Agent Profiles (Hermes)
```

## Quick Start

### Prerequisites
- Go 1.22+
- Or Docker

### Run with Go
```bash
cd 1claw-server
go mod tidy
go run . -config=config.yaml
```

### Run with Docker
```bash
docker build -t 1claw-server .
docker run -p 8080:8080 1claw-server
```

## Configuration

Edit `config.yaml` to set:
- Server host/port
- WebSocket settings
- Agent profiles (id, name, emoji, description, color)

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /health | Health check |
| GET | /api/status | Server + profile status |
| GET | /api/profiles | List all profiles |
| POST | /api/profiles | Create profile |
| PUT | /api/profiles/{id} | Update profile |
| DELETE | /api/profiles/{id} | Delete profile |
| WS | /ws | WebSocket connection |

## WebSocket Protocol

See [DESIGN.md](../DESIGN.md) for full protocol specification.

## Development

### Run tests
```bash
go test ./...
```

### Add new agent backend
Implement the `AgentBridge` interface in `internal/agent/` and swap in `main.go`.
