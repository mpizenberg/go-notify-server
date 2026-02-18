# go-notify-server

A minimal, self-hosted Web Push notification server in Go.

Handles VAPID key management, push subscription storage (SQLite), and notification delivery via the standard [Web Push protocol](https://www.rfc-editor.org/rfc/rfc8030) (RFC 8030 + VAPID RFC 8292). Designed to pair with any PWA that implements the browser Push API — no vendor lock-in, no external push service dependency beyond the browser-native ones (FCM, Mozilla Autopush, etc.).

## Why build this?

There is no off-the-shelf, lightweight, self-hosted server that implements RFC 8030 Web Push with subscription management and per-topic fan-out for **custom** PWAs. Existing tools either use the wrong protocol (Gotify → WebSocket, Mercure → SSE), only work with their own web app (ntfy.sh), or are abandoned. The "application server" role in Web Push is intentionally thin and application-specific — the ecosystem provides **libraries** for the hard cryptographic parts (`webpush-go`), not turnkey servers.

## Features

- Single static binary, no runtime dependencies
- Embedded SQLite storage (pure Go, no CGO)
- Per-topic subscriptions (not just broadcast)
- Automatic stale subscription cleanup (deletes on 404/410 from push services)
- Delivery logging with configurable log purge
- Simple bearer-token auth for admin endpoints
- CORS support for cross-origin apps
- Graceful shutdown (drains in-flight notifications)
- Multi-stage Docker image (~15 MB)

## Quick start

### Generate VAPID keys

```sh
# From source
go run . generate-vapid

# Or from Docker
docker run --rm ghcr.io/mpizenberg/go-notify-server generate-vapid
```

Output:

```
VAPID_PUBLIC_KEY=BLkzGx5k3Rq...
VAPID_PRIVATE_KEY=dGhpcyBpcyB...
```

### Run with Docker

```sh
docker run -d \
  -p 8080:8080 \
  -v notify-data:/data \
  -e VAPID_PUBLIC_KEY=... \
  -e VAPID_PRIVATE_KEY=... \
  -e VAPID_CONTACT=mailto:admin@example.com \
  -e ADMIN_KEY=your-secret-admin-key \
  ghcr.io/mpizenberg/go-notify-server
```

### Run from source

```sh
go build -o go-notify-server .

export VAPID_PUBLIC_KEY=...
export VAPID_PRIVATE_KEY=...
export VAPID_CONTACT=mailto:admin@example.com
export ADMIN_KEY=your-secret-admin-key

./go-notify-server
```

## Configuration

All via environment variables (12-factor style):

| Variable            | Required | Default            | Description                               |
| ------------------- | -------- | ------------------ | ----------------------------------------- |
| `VAPID_PUBLIC_KEY`  | yes      | —                  | Base64url-encoded ECDSA P-256 public key  |
| `VAPID_PRIVATE_KEY` | yes      | —                  | Base64url-encoded ECDSA P-256 private key |
| `VAPID_CONTACT`     | yes      | —                  | `mailto:` URI identifying the operator    |
| `ADMIN_KEY`         | yes      | —                  | Bearer token for admin endpoints          |
| `DB_PATH`           | no       | `./data/notify.db` | Path to the SQLite database file          |
| `PORT`              | no       | `8080`             | HTTP listen port                          |
| `CORS_ORIGIN`       | no       | `*`                | `Access-Control-Allow-Origin` value       |

## API

All endpoints accept and return `application/json`. Errors use `{"error": "message"}`.

### Public endpoints

These are called by your web app — no authentication required.

#### `GET /vapid-public-key`

Returns the server's VAPID public key for the client to call `pushManager.subscribe()`.

```json
{ "vapidPublicKey": "BLkzGx5k3Rq..." }
```

#### `POST /subscriptions`

Register or update a push subscription. The body matches `PushSubscription.toJSON()`:

```json
{
  "topic": "general",
  "subscription": {
    "endpoint": "https://fcm.googleapis.com/fcm/send/...",
    "keys": {
      "p256dh": "BNcRdreALRF...",
      "auth": "tBHItJI5svk..."
    }
  }
}
```

- `topic` is optional (defaults to `""`). Allows sending notifications to subsets of subscribers.
- Returns `201 Created` with `{"id": "..."}` for new subscriptions, `200 OK` for updates.

#### `DELETE /subscriptions`

Unregister a subscription by endpoint:

```json
{ "endpoint": "https://fcm.googleapis.com/fcm/send/..." }
```

Returns `204 No Content`. Idempotent (returns 204 even if not found).

### Admin endpoints

Require `Authorization: Bearer <ADMIN_KEY>`. Return `401` if missing or invalid.

#### `POST /notify`

Send a push notification to matching subscriptions:

```json
{
  "topic": "general",
  "title": "New message",
  "body": "You have a new message from Alice",
  "icon": "/icons/icon-192.png",
  "badge": "/icons/badge-72.png",
  "tag": "message-123",
  "url": "/messages/123"
}
```

- `title` is required. All other fields are optional.
- If `topic` is set, only matching subscriptions are notified. If omitted, all subscriptions are notified.
- `url` is nested under `data.url` in the push payload sent to browsers.
- Delivery fans out concurrently (pool of 10). Stale subscriptions (404/410) are automatically removed.
- TTL: 24 hours for all messages.

Response:

```json
{ "sent": 42, "failed": 1, "stale_removed": 1 }
```

#### `GET /subscriptions?topic=...`

List subscriptions (keys omitted for security). Optional `topic` query parameter to filter.

```json
{
  "subscriptions": [
    {
      "id": "a1b2c3...",
      "topic": "general",
      "endpoint": "https://...",
      "created_at": "2025-06-15 10:30:00"
    }
  ]
}
```

#### `DELETE /subscriptions/{id}`

Remove a subscription by ID. Returns `204 No Content`.

#### `DELETE /delivery-log?older_than=30d`

Purge delivery log entries. `older_than` accepts `Nd`, `Nh`, `Nm` (default `30d`).

```json
{ "deleted": 1523 }
```

## Database

Single SQLite database (WAL mode, 5s busy timeout), two tables created on startup:

```sql
CREATE TABLE subscriptions (
    id         TEXT PRIMARY KEY,
    topic      TEXT NOT NULL DEFAULT '',
    endpoint   TEXT NOT NULL,
    key_p256dh TEXT NOT NULL,
    key_auth   TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(endpoint)
);

CREATE TABLE delivery_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id TEXT NOT NULL,
    sent_at         TEXT NOT NULL DEFAULT (datetime('now')),
    status_code     INTEGER NOT NULL,
    error           TEXT NOT NULL DEFAULT ''
);
```

## Docker

Multi-stage build producing a minimal Alpine image:

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /go-notify-server .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /go-notify-server /usr/local/bin/go-notify-server
VOLUME /data
ENV DB_PATH=/data/notify.db
EXPOSE 8080
ENTRYPOINT ["go-notify-server"]
```

`CGO_ENABLED=0` works because `modernc.org/sqlite` is a pure-Go SQLite implementation. The `/data` volume persists the database across container restarts.

Container images are published to `ghcr.io/mpizenberg/go-notify-server` on version tags.

## Deployment

Web Push **requires HTTPS** — browsers refuse `pushManager.subscribe()` on insecure origins. The server itself listens on plain HTTP (port 8080); use a reverse proxy for TLS termination.

### Docker Compose

A typical setup with Caddy as the reverse proxy handling automatic HTTPS:

```yaml
services:
  notify:
    image: ghcr.io/mpizenberg/go-notify-server
    restart: unless-stopped
    volumes:
      - notify-data:/data
    environment:
      VAPID_PUBLIC_KEY: ${VAPID_PUBLIC_KEY}
      VAPID_PRIVATE_KEY: ${VAPID_PRIVATE_KEY}
      VAPID_CONTACT: ${VAPID_CONTACT}
      ADMIN_KEY: ${ADMIN_KEY}
      CORS_ORIGIN: https://myapp.example.com

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - caddy-data:/data
      - ./Caddyfile:/etc/caddy/Caddyfile

volumes:
  notify-data:
  caddy-data:
```

With a `Caddyfile`:

```
push.example.com {
    reverse_proxy notify:8080
}
```

Put the secrets in a `.env` file next to the compose file:

```sh
VAPID_PUBLIC_KEY=BLkzGx5k3Rq...
VAPID_PRIVATE_KEY=dGhpcyBpcyB...
VAPID_CONTACT=mailto:admin@example.com
ADMIN_KEY=your-secret-admin-key
```

### Dokploy

[Dokploy](https://dokploy.com) manages Docker Compose deployments with Traefik handling TLS via Let's Encrypt.

1. **Create a Compose project** — In your Dokploy dashboard, create a new **Compose** project. Paste the following as the compose file:

   ```yaml
   services:
     notify:
       image: ghcr.io/mpizenberg/go-notify-server
       restart: unless-stopped
       volumes:
         - notify-data:/data
       environment:
         VAPID_PUBLIC_KEY: ${VAPID_PUBLIC_KEY}
         VAPID_PRIVATE_KEY: ${VAPID_PRIVATE_KEY}
         VAPID_CONTACT: ${VAPID_CONTACT}
         ADMIN_KEY: ${ADMIN_KEY}
         CORS_ORIGIN: ${CORS_ORIGIN}

   volumes:
     notify-data:
   ```

2. **Environment variables** — In the "Environment" tab, add:

   ```
   VAPID_PUBLIC_KEY=BLkzGx5k3Rq...
   VAPID_PRIVATE_KEY=dGhpcyBpcyB...
   VAPID_CONTACT=mailto:admin@example.com
   ADMIN_KEY=your-secret-admin-key
   CORS_ORIGIN=https://myapp.example.com
   ```

3. **Domain** — In the "Domains" tab, add your domain (e.g. `push.example.com`) pointed at the `notify` service on port 8080. Dokploy configures Traefik to route traffic and provision a Let's Encrypt certificate automatically.

4. **Deploy** — Hit deploy. Traefik proxies HTTPS traffic to the container on port 8080, and the `notify-data` volume persists the SQLite database across redeployments.

### Production notes

- **Set `CORS_ORIGIN`** to your app's actual origin (e.g. `https://myapp.example.com`). The default `*` is fine for development but too permissive for production.
- **Back up the SQLite database** — the `/data/notify.db` file is the only state. A simple file copy while the server is running is safe (SQLite WAL mode).
- **Delivery logs** are automatically purged every 24 hours (entries older than 30 days are deleted). You can also trigger a manual purge via `DELETE /delivery-log?older_than=30d`.

## Development

### Build and test

```sh
go build ./...
go test ./...
```

### Project structure

```
go-notify-server/
├── main.go          # entry point, CLI, env config, startup, graceful shutdown
├── server.go        # routing (Go 1.22+ ServeMux), middleware (CORS, logging, content-type)
├── handlers.go      # HTTP endpoint handlers, Server struct, auth middleware
├── db.go            # SQLite open, migrate, CRUD operations
├── push.go          # web-push fan-out delivery, stale cleanup, delivery logging
├── vapid.go         # VAPID key generation and parsing
├── main_test.go     # tests (VAPID, DB, upsert, HTTP handlers)
├── Dockerfile       # multi-stage container build
├── go.mod / go.sum
└── .github/workflows/ci.yml  # CI: build/test + container publish
```

Single `package main` — no `internal/` or `pkg/` directories. The full server is ~500 lines of Go.

### Dependencies

| Dependency                                                    | Purpose                                                              |
| ------------------------------------------------------------- | -------------------------------------------------------------------- |
| [`webpush-go`](https://github.com/SherClockHolmes/webpush-go) | Web Push protocol (VAPID signing, payload encryption, HTTP delivery) |
| [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) | Pure-Go SQLite driver (no CGO, single binary)                        |
| Go standard library                                           | HTTP server, JSON, crypto, CLI                                       |

### CI

GitHub Actions runs on every push and PR:

1. **build-and-test** — `go vet`, `go build`, `go test`
2. **docker** — builds the container image on main; builds and pushes to `ghcr.io` on `v*` tags

## Usage from a web app

1. `GET /vapid-public-key` to retrieve the VAPID key
2. Pass it to `pushManager.subscribe({applicationServerKey: vapidKey})`
3. `POST /subscriptions` with the resulting `PushSubscription.toJSON()`
4. To unsubscribe: `DELETE /subscriptions` with the endpoint

### Sending a test notification

```sh
curl -X POST http://localhost:8080/notify \
  -H "Authorization: Bearer your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{"title": "Test", "body": "Hello from go-notify-server"}'
```

### Listing subscriptions

```sh
curl -H "Authorization: Bearer your-admin-key" \
  http://localhost:8080/subscriptions
```

## Graceful shutdown

On `SIGINT` / `SIGTERM`:

1. Stop accepting new connections (10s timeout)
2. Wait for in-flight notification deliveries to complete
3. Close SQLite connection
4. Exit 0
