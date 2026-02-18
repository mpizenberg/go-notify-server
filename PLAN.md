# Implementation Plan: go-notify-server

## Context

Build a minimal self-hosted Web Push notification server in Go from scratch, following the spec in `go-notify-server.md`. The project directory currently has no Go code — only the spec and research report.

## Dependencies

- `github.com/SherClockHolmes/webpush-go` — Web Push protocol (VAPID, encryption, delivery)
- `modernc.org/sqlite` — Pure-Go SQLite driver (no CGO, single binary)
- Go 1.23 standard library (`net/http` with 1.22+ ServeMux pattern matching)

## Implementation Order

| Step | File | What it does |
|------|------|-------------|
| 1 | `go.mod` | `go mod init` + `go get` dependencies |
| 2 | `vapid.go` | VAPID key generation and parsing/validation |
| 3 | `db.go` | SQLite open, migrate, CRUD (subscriptions + delivery_log) |
| 4 | `push.go` | Fan-out notification delivery (bounded goroutine pool of 10, stale cleanup, delivery logging) |
| 5 | `handlers.go` | HTTP endpoint handlers + `Server` struct holding shared deps |
| 6 | `server.go` | Routing (Go 1.22+ ServeMux) + middleware (CORS, logging, auth, content-type) |
| 7 | `main.go` | CLI parsing, env var config, startup wiring, graceful shutdown |
| 8 | `Dockerfile` | Multi-stage build (golang:1.23-alpine → alpine:3.20) |

## Key Design Decisions

**Routing:** Go 1.22+ ServeMux patterns separate method+path cleanly:
- `POST /subscriptions` (public) vs `GET /subscriptions` (admin) vs `DELETE /subscriptions` (public) vs `DELETE /subscriptions/{id}` (admin) — all distinct patterns, no conflicts
- Admin auth applied per-route (wrapping individual handlers), not globally

**Graceful shutdown:** `SendNotifications` uses `context.Background()` (not request context) so in-flight push delivery survives `http.Server.Shutdown`. A `sync.WaitGroup` on the `Server` struct tracks active fan-outs; main waits on it after shutdown.

**Push payload:** The `url` field from the notify request is nested under `data.url` in the push payload sent to browsers, matching the service worker's expected format.

**SQLite:** WAL mode + busy_timeout=5000ms. Parent directory created automatically via `os.MkdirAll`.

**Subscription upsert:** `INSERT ... ON CONFLICT(endpoint) DO UPDATE` — atomic, returns whether created or updated to set 201 vs 200 status.

## Verification

1. `go build .` — must compile with no errors
2. `./go-notify-server generate-vapid` — prints VAPID keypair
3. Start server with env vars set, verify it logs `listening on :8080`
4. `curl localhost:8080/vapid-public-key` — returns the public key
5. `curl -X POST localhost:8080/subscriptions -H 'Content-Type: application/json' -d '{"topic":"test","subscription":{"endpoint":"https://example.com/push","keys":{"p256dh":"test","auth":"test"}}}'` — returns 201 with ID
6. `curl -H 'Authorization: Bearer <key>' localhost:8080/subscriptions` — lists the subscription
7. Send SIGINT — graceful shutdown sequence logs and exits 0
