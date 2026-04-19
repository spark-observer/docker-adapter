# Changelog

## 2026-04-19

### Fixed
- Prevented concurrent WebSocket writes by introducing per-client buffered send queues and dedicated writer handling.
- Added backpressure handling for slow clients by dropping messages when client buffers are full.
- Switched stream registry locking to `sync.RWMutex` and read locks for read-only paths.
- Reworked stream lifecycle to use `context.WithCancel` + `exec.CommandContext` for cleaner stop behavior.
- Moved stdout/stderr pipe setup before process start and added error handling/cleanup for pipe/start failures.
- Added `MAX_STREAMS` (default `50`) to cap concurrent `docker logs -f` followers.
- Preserved container names during stale stream removal events in reconcile.
- Improved shutdown to stop ticker and terminate active streams before process exit.

