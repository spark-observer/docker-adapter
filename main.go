package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	port        int
	labelFilter string
	rescanMs    int
	maxStreams  int
)

func init() {
	port = envInt("PORT", 9090)
	labelFilter = envStr("LABEL_FILTER", "publishLog=true")
	rescanMs = envInt("RESCAN_MS", 5000)
	maxStreams = envInt("MAX_STREAMS", 50)
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ── Message types ─────────────────────────────────────────────────────────────

type Message map[string]any

// ts returns a UTC RFC3339 timestamp used in all outbound events.
func ts() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ── Client hub ────────────────────────────────────────────────────────────────

const clientSendBuf = 64

type client struct {
	conn *websocket.Conn
	send chan []byte
}

func newClient(conn *websocket.Conn) *client {
	c := &client{conn: conn, send: make(chan []byte, clientSendBuf)}
	go func() {
		for data := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				break
			}
		}
		conn.Close()
	}()
	return c
}

func (c *client) write(data []byte) {
	select {
	case c.send <- data:
	default:
		log.Printf("client %s send buffer full; dropping message", c.conn.RemoteAddr())
	}
}

func (c *client) close() {
	close(c.send)
}

// Hub tracks active WebSocket clients and fan-outs messages to all of them.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

func newHub() *Hub { return &Hub{clients: make(map[*client]struct{})} }

func (h *Hub) add(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.close()
}

// broadcast sends one JSON payload to every currently connected client.
func (h *Hub) broadcast(msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.write(data)
	}
}

func (h *Hub) size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ── Stream registry ───────────────────────────────────────────────────────────

// container holds minimal metadata required for stream lifecycle events.
type container struct {
	id   string
	name string
}

// streamEntry tracks a running `docker logs -f` process per container.
type streamEntry struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	name   string
}

// Registry stores active log streams keyed by container ID.
type Registry struct {
	mu      sync.RWMutex
	streams map[string]*streamEntry
}

func newRegistry() *Registry {
	return &Registry{streams: make(map[string]*streamEntry)}
}

func (r *Registry) has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.streams[id]
	return ok
}

func (r *Registry) set(id string, e *streamEntry) {
	r.mu.Lock()
	r.streams[id] = e
	r.mu.Unlock()
}

func (r *Registry) delete(id string) {
	r.mu.Lock()
	delete(r.streams, id)
	r.mu.Unlock()
}

func (r *Registry) liveIds() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.streams))
	for id, entry := range r.streams {
		out[id] = entry.name
	}
	return out
}

func (r *Registry) get(id string) (*streamEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.streams[id]
	return e, ok
}

func (r *Registry) size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.streams)
}

// ── Docker helpers ────────────────────────────────────────────────────────────

// listContainers returns running containers that match LABEL_FILTER.
func listContainers(filter string) ([]container, error) {
	out, err := exec.Command(
		"docker", "ps",
		"--filter", "label="+filter,
		"--format", "{{.ID}} {{.Names}}",
	).Output()
	if err != nil {
		return nil, err
	}

	var containers []container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		c := container{id: parts[0]}
		if len(parts) == 2 {
			c.name = parts[1]
		}
		containers = append(containers, c)
	}
	return containers, nil
}

// ── Log streaming ─────────────────────────────────────────────────────────────

// startStream attaches to `docker logs -f` for one container and emits events.
// A stream is started only once per container ID.
func startStream(c container, reg *Registry, hub *Hub) {
	if reg.has(c.id) {
		return
	}
	if reg.size() >= maxStreams {
		hub.broadcast(Message{
			"type":          "stream_skipped",
			"reason":        "max_streams_reached",
			"containerId":   c.id,
			"containerName": c.name,
			"maxStreams":    maxStreams,
			"ts":            ts(),
		})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--timestamps", c.id)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		hub.broadcast(Message{
			"type":    "error",
			"message": fmt.Sprintf("failed creating stdout pipe for %s: %v", c.id, err),
			"ts":      ts(),
		})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		cancel()
		hub.broadcast(Message{
			"type":    "error",
			"message": fmt.Sprintf("failed creating stderr pipe for %s: %v", c.id, err),
			"ts":      ts(),
		})
		return
	}

	reg.set(c.id, &streamEntry{cmd: cmd, cancel: cancel, name: c.name})

	hub.broadcast(Message{
		"type":          "stream_started",
		"containerId":   c.id,
		"containerName": c.name,
		"labelFilter":   labelFilter,
		"ts":            ts(),
	})

	go func() {
		if err := cmd.Start(); err != nil {
			_ = stdout.Close()
			_ = stderr.Close()
			hub.broadcast(Message{
				"type":    "error",
				"message": fmt.Sprintf("failed to start log stream for %s: %v", c.id, err),
				"ts":      ts(),
			})
			reg.delete(c.id)
			cancel()
			return
		}

		// streamLines forwards each line from stdout/stderr as a `log` event.
		streamLines := func(scanner *bufio.Scanner, streamName string) {
			for scanner.Scan() {
				line := scanner.Text()
				if strings.TrimSpace(line) == "" {
					continue
				}
				hub.broadcast(Message{
					"type":          "log",
					"stream":        streamName,
					"containerId":   c.id,
					"containerName": c.name,
					"labelFilter":   labelFilter,
					"line":          line,
					"ts":            ts(),
				})
			}
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); streamLines(bufio.NewScanner(stdout), "stdout") }()
		go func() { defer wg.Done(); streamLines(bufio.NewScanner(stderr), "stderr") }()
		wg.Wait()

		code := 0
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			}
		}

		reg.delete(c.id)
		cancel()
		hub.broadcast(Message{
			"type":          "stream_closed",
			"containerId":   c.id,
			"containerName": c.name,
			"code":          code,
			"ts":            ts(),
		})
	}()
}

func stopStream(id string, name string, reg *Registry, hub *Hub) {
	entry, ok := reg.get(id)
	if !ok {
		return
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.cmd.Process != nil {
		_ = entry.cmd.Process.Kill()
	}
	reg.delete(id)
	hub.broadcast(Message{
		"type":          "stream_removed",
		"containerId":   id,
		"containerName": name,
		"ts":            ts(),
	})
}

// ── Reconciler ────────────────────────────────────────────────────────────────

// reconcile aligns active streams with the currently matching Docker containers.
func reconcile(reg *Registry, hub *Hub) {
	containers, err := listContainers(labelFilter)
	if err != nil {
		hub.broadcast(Message{
			"type":    "error",
			"message": "Cannot list containers: " + err.Error(),
			"ts":      ts(),
		})
		return
	}

	liveIds := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		liveIds[c.id] = struct{}{}
		startStream(c, reg, hub)
	}

	// Remove streams for containers that are no longer running / matching
	for id, name := range reg.liveIds() {
		if _, alive := liveIds[id]; !alive {
			stopStream(id, name, reg, hub)
		}
	}
}

// ── WebSocket handler ─────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{
	// NOTE: this accepts all origins; keep as-is for parity with current behavior.
	// Restrict in production deployments.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsHandler upgrades requests to WebSocket and sends an initial `connected` event.
func wsHandler(hub *Hub, reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}

		c := newClient(conn)
		hub.add(c)
		defer hub.remove(c)

		// Send initial "connected" message
		data, _ := json.Marshal(Message{
			"type":          "connected",
			"labelFilter":   labelFilter,
			"rescanMs":      rescanMs,
			"activeStreams": reg.size(),
			"ts":            ts(),
		})
		c.write(data)

		// Drain incoming messages (keep connection alive)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// hub: connected WebSocket clients; reg: active docker log followers.
	hub := newHub()
	reg := newRegistry()

	http.HandleFunc("/", wsHandler(hub, reg))

	// Initial reconcile
	reconcile(reg, hub)

	// Periodic reconcile
	ticker := time.NewTicker(time.Duration(rescanMs) * time.Millisecond)
	go func() {
		for range ticker.C {
			reconcile(reg, hub)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Println("Shutting down...")
		ticker.Stop()
		for id, name := range reg.liveIds() {
			stopStream(id, name, reg, hub)
		}
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%d", port)
	log.Printf("WebSocket server listening on ws://localhost%s", addr)
	log.Printf("Streaming logs for Docker containers with label: %s", labelFilter)
	log.Printf("Rescan interval: %dms | Max streams: %d | Press Ctrl+C to stop", rescanMs, maxStreams)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
