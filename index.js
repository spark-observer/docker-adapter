import { exec, spawn } from 'child_process';
import { WebSocketServer } from 'ws';
import  dotenv from "dotenv";

dotenv.config();

const PORT = Number(process.env.PORT || 8080);
const LABEL_FILTER = process.env.LABEL_FILTER || 'publishLog=true';
const RESCAN_MS = Number(process.env.RESCAN_MS || 5000);

const wss = new WebSocketServer({ port: PORT });
const activeStreams = new Map();

function broadcast(payload) {
  const message = JSON.stringify(payload);
  for (const client of wss.clients) {
    if (client.readyState === 1) client.send(message);
  }
}

function listMatchingContainers() {
  return new Promise((resolve) => {
    exec(`docker ps --filter "label=${LABEL_FILTER}" --format "{{.ID}} {{.Names}}"`, (err, stdout, stderr) => {
      if (err) {
        broadcast({ type: 'error', message: stderr || err.message, ts: new Date().toISOString() });
        return resolve([]);
      }
      const containers = stdout
        .trim()
        .split('\n')
        .filter(Boolean)
        .map(line => {
          const [id, ...rest] = line.split(' ');
          return { id, name: rest.join(' ') };
        });
      resolve(containers);
    });
  });
}

function startLogStream(container) {
  if (activeStreams.has(container.id)) return;

  const child = spawn('docker', ['logs', '-f', '--timestamps', container.id]);
  activeStreams.set(container.id, { child, container });

  let stdoutBuffer = '';
  let stderrBuffer = '';

  const emitLines = (chunk, streamName) => {
    const text = chunk.toString();
    let buffer = streamName === 'stderr' ? stderrBuffer + text : stdoutBuffer + text;
    const parts = buffer.split('\n');
    buffer = parts.pop() || '';

    for (const line of parts) {
      if (!line.trim()) continue;
      broadcast({
        type: 'log',
        stream: streamName,
        containerId: container.id,
        containerName: container.name,
        labelFilter: LABEL_FILTER,
        line,
        ts: new Date().toISOString()
      });
    }

    if (streamName === 'stderr') stderrBuffer = buffer;
    else stdoutBuffer = buffer;
  };

  child.stdout.on('data', chunk => emitLines(chunk, 'stdout'));
  child.stderr.on('data', chunk => emitLines(chunk, 'stderr'));

  child.on('close', code => {
    activeStreams.delete(container.id);
    broadcast({
      type: 'stream_closed',
      containerId: container.id,
      containerName: container.name,
      code,
      ts: new Date().toISOString()
    });
  });

  broadcast({
    type: 'stream_started',
    containerId: container.id,
    containerName: container.name,
    labelFilter: LABEL_FILTER,
    ts: new Date().toISOString()
  });
}

async function reconcileStreams() {
  const containers = await listMatchingContainers();
  const liveIds = new Set(containers.map(c => c.id));

  for (const container of containers) startLogStream(container);

  for (const [id, entry] of activeStreams.entries()) {
    if (!liveIds.has(id)) {
      entry.child.kill();
      activeStreams.delete(id);
      broadcast({
        type: 'stream_removed',
        containerId: id,
        containerName: entry.container.name,
        ts: new Date().toISOString()
      });
    }
  }
}

wss.on('connection', ws => {
  ws.send(JSON.stringify({
    type: 'connected',
    labelFilter: LABEL_FILTER,
    rescanMs: RESCAN_MS,
    activeStreams: activeStreams.size,
    ts: new Date().toISOString()
  }));
});

setInterval(reconcileStreams, RESCAN_MS);
reconcileStreams();

console.log(`WebSocket server listening on ws://localhost:${PORT}`);
console.log(`Streaming logs for Docker containers with label ${LABEL_FILTER}`);