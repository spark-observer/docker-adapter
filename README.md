# 🐳 docker-log-adapter

> A lightweight WebSocket server that streams real-time logs from all Docker containers labeled `publishLog=true` on the host machine.







> ⚠️ **Security Notice**: This project has known security considerations for production use. See [SECURITY_ISSUES.md](./doc/SECURITY_ISSUES.md) before deploying.

***

## 📖 Table of Contents

- [How It Works](#how-it-works)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Configuration](#configuration)
- [Running the Server](#running-the-server)
- [Running with Docker](#running-with-docker)
- [WebSocket Message Format](#websocket-message-format)
- [Generating Test Logs](#generating-test-logs)
- [Known Issues & Security](#known-issues--security)

***

## How It Works

```
┌─── HOST ─────────────────────────────────────────────────────────┐
│                                                                   │
│   ┌── docker-log-adapter ──────────────────────────────────┐     │
│   │                                                        │     │
│   │  1. Every 5s: docker ps --filter "label=publishLog=true│     │
│   │  2. For each match: docker logs -f --timestamps        │     │
│   │  3. Each new log line → broadcast over WebSocket       │     │
│   │                                                        │     │
│   │  ws://localhost:9090  ◄──── your backend / client      │     │
│   └────────────────────────────────────────────────────────┘     │
│                                                                   │
│   ┌── app1 (publishLog=true) ──┐  ┌── app2 (publishLog=true) ──┐ │
│   │   nginx:alpine             │  │   your-service             │ │
│   └────────────────────────────┘  └────────────────────────────┘ │
└───────────────────────────────────────────────────────────────────┘
```

The adapter uses Docker label filtering to discover containers and streams their logs in real time via a persistent `docker logs -f` process per container. New containers are detected automatically within the rescan interval.

***

## Prerequisites

Before running this script, ensure the following are in place:

### 1. Node.js 18+
### 1. Go 1.22+
```bash
go version   # should be go1.22.0 or higher
```
Install from [go.dev/dl](https://go.dev/dl) or via your system package manager.

### 2. Docker installed and running
```bash
docker --version
docker ps   # should work without errors
```

### 3. Docker daemon access (host user)
Your host user must be allowed to run Docker commands (`docker ps`, `docker compose up`).

If you see:
```
permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock
```
Fix it by adding your user to the `docker` group:
```bash
sudo usermod -aG docker $USER
newgrp docker
docker ps   # verify it works
```

> In Compose mode, the adapter **runs as non-root** (`appuser:10001`) and does **not** mount the raw host socket directly; it talks to the `socket-proxy` service via `DOCKER_HOST=tcp://socket-proxy:2375`.

### 4. Port available
Default WebSocket port is `9090`. Make sure it is not occupied:
```bash
lsof -i :9090
```

### 5. At least one labeled container running
Your target containers must be started with the `publishLog=true` label:
```bash
docker run -d \
  --label publishLog=true \
  --name app1 \
  -p 8080:80 \
  nginx:alpine
```

***

## Installation

```bash
# Clone or copy the project
cd docker-log-adapter

# Download dependencies
go mod download

# Build the binary
go build -o docker-log-adapter .
```

***

## Configuration

Set environment variables before running the binary. All values have sensible defaults.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `9090` | WebSocket server port |
| `LABEL_FILTER` | `publishLog=true` | Docker label filter for container discovery |
| `RESCAN_MS` | `5000` | Interval (ms) to scan for new/removed containers |

***

## Running the Server

```bash
# Default settings
./docker-log-adapter

# Custom port
PORT=9091 ./docker-log-adapter

# Custom label filter
LABEL_FILTER=logging=enabled ./docker-log-adapter

# All env vars inline
PORT=9091 LABEL_FILTER=logging=enabled RESCAN_MS=3000 ./docker-log-adapter
```
You should see:
```
WebSocket server listening on ws://localhost:9090
Streaming logs for Docker containers with label: publishLog=true
Rescan interval: 5000ms | Press Ctrl+C to stop
```

Press `Ctrl+C` for graceful shutdown — all streams and the port are released cleanly.

***

## Running with Docker

### Using Docker Compose (recommended)

```bash
docker compose up -d
```

This Compose setup uses a Docker socket proxy and sets `DOCKER_HOST=tcp://socket-proxy:2375` for the adapter, so the adapter no longer mounts the raw host Docker socket directly.

### Using plain Docker run

```bash
docker build -t docker-log-adapter .

docker run -d \
  --name docker-log-adapter \
  -p 9090:9090 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e LABEL_FILTER=publishLog=true \
  -e RESCAN_MS=5000 \
  docker-log-adapter
```

> ⚠️ The plain `docker run` example mounts the raw Docker socket and is less secure. Prefer Docker Compose for production, and review [SECURITY_ISSUES.md](./doc/SECURITY_ISSUES.md).

***

## WebSocket Message Format

Connect to `ws://localhost:9090` using any WebSocket client.

### Message types

#### `connected` — sent on successful connection
```json
{
  "type": "connected",
  "labelFilter": "publishLog=true",
  "rescanMs": 5000,
  "activeStreams": 2,
  "ts": "2026-04-18T00:00:00.000Z"
}
```

#### `stream_started` — new container discovered and being followed
```json
{
  "type": "stream_started",
  "containerId": "e601b13de8e6",
  "containerName": "app1",
  "labelFilter": "publishLog=true",
  "ts": "2026-04-18T00:00:01.000Z"
}
```

#### `log` — new log line from a container
```json
{
  "type": "log",
  "stream": "stdout",
  "containerId": "e601b13de8e6",
  "containerName": "app1",
  "labelFilter": "publishLog=true",
  "line": "2026-04-18T00:00:02.123456789Z 172.17.0.1 - GET / HTTP/1.1 200",
  "ts": "2026-04-18T00:00:07.000Z"
}
```

#### `stream_closed` — container stopped or exited
```json
{
  "type": "stream_closed",
  "containerId": "e601b13de8e6",
  "containerName": "app1",
  "code": 0,
  "ts": "2026-04-18T00:01:00.000Z"
}
```

#### `stream_removed` — container no longer matches label filter
```json
{
  "type": "stream_removed",
  "containerId": "e601b13de8e6",
  "containerName": "app1",
  "ts": "2026-04-18T00:01:05.000Z"
}
```

#### `error` — Docker command failed
```json
{
  "type": "error",
  "message": "Cannot connect to Docker daemon",
  "ts": "2026-04-18T00:00:00.000Z"
}
```

***

## Generating Test Logs

Start a labeled nginx container and send requests to it:

```bash
# Start container
docker run -d \
  --label publishLog=true \
  --name app1 \
  -p 8080:80 \
  nginx:alpine

# Generate continuous logs (one request per second)
while true; do
  curl -s http://localhost:8080/test-$RANDOM > /dev/null
  sleep 1
done
```

***

## Known Issues & Security

This project has **8 open issues** including security concerns around Docker socket access, missing WebSocket authentication, cleartext log transmission, and log content leakage.

👉 **[View full ISSUES.md](./ISSUES.md)**

| Severity | Count |
|---|---|
|  | 1 |
|  | 2 |
|  | 3 |
|  | 2 |

***

*Part of the spark-prime infra observability platform.*