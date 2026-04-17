# Security Issues

## SEC-0000. Docker socket = root on host

`Current Status`: <span style="background-color:red;color:white;padding:2px 6px;border-radius:6px;font-weight:bold;font-size:0.75rem">OPEN</span>

Yes — there are serious security concerns with mounting /var/run/docker.sock in production. Here's a complete breakdown:

The core risk — Docker socket = root on host
Mounting the Docker socket into a container is essentially giving that container unrestricted root access to the host machine. Anyone who can reach that container can:

- Start/stop/delete any container on the host
- Mount host filesystem into a new container and read/write anything
- Run a privileged container and escape to host root
- Extract secrets and environment variables from other containers
- Take over the entire host with a single docker run command

```bash
# An attacker inside your adapter container can do this:
docker run -v /:/host --rm -it alpine chroot /host
# → They now have full root shell on your HOST machine
```
| Risk                       | Why                                                                                            |
| -------------------------- | ---------------------------------------------------------------------------------------------- |
| Full Docker daemon access  | docker ps and docker logs use the same socket that can also docker run, docker exec, docker rm |
| No auth on WebSocket       | Your script has no auth — anyone who reaches port 9090 can receive all container logs          |
| Log data leakage           | Logs may contain secrets, API keys, DB passwords, JWT tokens                                   |
| Container name/ID exposure | Broadcasts container IDs and names to all WebSocket clients                                    |
| No TLS on WebSocket        | Plain ws:// means logs are transmitted unencrypted over the network                            |
| No rate limiting           | Unlimited clients can connect and consume all log data                                         |

### Mitigations — from easiest to strongest
#### Fix 1 — Use Docker Socket Proxy (most important)
nstead of mounting the raw socket, put a read-only proxy in front of it that allows only the specific API calls you need:
```yaml
# docker-compose.yml
services:
  socket-proxy:
    image: tecnativa/docker-socket-proxy
    environment:
      CONTAINERS: 1   # allow GET /containers
      EXEC: 0         # block docker exec
      IMAGES: 0       # block image access
      NETWORKS: 0
      VOLUMES: 0
      POST: 0         # block all write/delete operations
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    networks:
      - proxy-net

  docker-log-adapter:
    build: .
    environment:
      - DOCKER_HOST=tcp://socket-proxy:2375  # use proxy, not raw socket
    networks:
      - proxy-net
    # NO socket mount here

networks:
  proxy-net:
    internal: true  # proxy network not exposed externally
```
#### Fix 2 — Add WebSocket authentication
Add a token check before allowing any client to receive logs:

```javascript
wss.on('connection', (ws, req) => {
  const url = new URL(req.url, 'http://localhost');
  const token = url.searchParams.get('token');

  if (token !== process.env.WS_SECRET_TOKEN) {
    ws.close(4001, 'Unauthorized');
    return;
  }

  ws.send(JSON.stringify({ type: 'connected', ... }));
});
```
Connect with:

```text
ws://localhost:9090?token=your-secret-token
```
#### Fix 3 — Add TLS (wss://)
Wrap the WebSocket server with HTTPS so logs are not sent in plain text:

```javascript
import https from 'https';
import fs from 'fs';

const server = https.createServer({
  cert: fs.readFileSync('/certs/cert.pem'),
  key:  fs.readFileSync('/certs/key.pem')
});

const wss = new WebSocketServer({ server });
server.listen(PORT);
```
#### Fix 4 — Limit log content
Strip sensitive patterns before broadcasting:

```javascript
const REDACT = [
  /password=\S+/gi,
  /token=\S+/gi,
  /Authorization:\s*\S+/gi,
  /Bearer\s+\S+/gi,
  /secret=\S+/gi,
];

function redact(line) {
  return REDACT.reduce((l, pattern) => l.replace(pattern, '[REDACTED]'), line);
}
```
#### Fix 5 — Drop root, use docker group
In your Dockerfile, instead of running as root:

```text
# Create non-root user and add to docker group (GID 999 is typical for docker)
RUN addgroup -S appgroup && \
    adduser -S appuser -G appgroup && \
    addgroup appuser docker || true

USER appuser
Or match the host docker group GID:
```
```bash
docker run --group-add $(stat -c '%g' /var/run/docker.sock) ...
```
#### Fix 6 — Restrict WebSocket access with firewall
Make sure port 9090 is not publicly accessible. It should only be reachable from your backend service:

```bash
# Only allow your backend IP to connect
ufw allow from 10.0.0.5 to any port 9090
ufw deny 9090
```
Or in docker-compose, don't publish the port externally — use an internal Docker network:

```yaml
services:
  docker-log-adapter:
    ports: []  # no host port binding
    networks:
      - internal-net

  your-backend:
    networks:
      - internal-net  # backend connects via ws://docker-log-adapter:9090

networks:
  internal-net:
    internal: true
```
**Priority order for your project**

For your AI infra project specifically, I would fix in this order:

1. Socket proxy — eliminates the biggest risk immediately
2. WS token auth — protects log data from unauthorized access
3. Internal network only — don't expose port publicly
4. Log redaction — protect secrets in log content
5. TLS — add when you move to production deployment

The socket proxy + token auth + internal network combination makes this production-safe for most internal observability use cases.

<hr>

