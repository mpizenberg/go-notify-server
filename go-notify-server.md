# go-notify-server

A minimal self-hosted web push notification server in Go.

Handles VAPID key management, push subscription storage, and notification delivery.
Designed to pair with an Elm PWA (or any web app) that manages the client-side Push API.

## Goals

- Single static binary, no runtime dependencies
- SQLite storage (embedded, no external DB)
- Docker image for Dokploy deployment
- Per-topic subscriptions (not just broadcast)
- Simple bearer-token auth for admin endpoints
- CORS support for cross-origin Elm apps

## Non-goals

- User accounts / authentication on the subscriber side
- Web UI / dashboard
- Websockets or SSE
- Payload encryption library from scratch (use an existing Go web-push library)

## Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/SherClockHolmes/webpush-go` | Web Push protocol (VAPID signing, payload encryption, HTTP delivery) |
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO, single binary) |
| Standard library (`net/http`, `encoding/json`, `crypto/ecdsa`, `flag`) | HTTP server, JSON, CLI |

No web framework — use `net/http` with a small hand-rolled router or `http.ServeMux` (Go 1.22+ pattern matching).

## Configuration

All via environment variables (12-factor style):

| Variable | Required | Default | Description |
|---|---|---|---|
| `VAPID_PUBLIC_KEY` | yes* | — | Base64url-encoded ECDSA P-256 public key |
| `VAPID_PRIVATE_KEY` | yes* | — | Base64url-encoded ECDSA P-256 private key |
| `VAPID_CONTACT` | yes | — | `mailto:` URI identifying the operator (e.g., `mailto:admin@example.com`) |
| `ADMIN_KEY` | yes | — | Bearer token required for admin endpoints (`POST /notify`, `GET /subscriptions`, `DELETE /subscriptions/:id`) |
| `DB_PATH` | no | `./data/notify.db` | Path to the SQLite database file |
| `PORT` | no | `8080` | HTTP listen port |
| `CORS_ORIGIN` | no | `*` | `Access-Control-Allow-Origin` value (set to your app's origin in production) |

*If `VAPID_PUBLIC_KEY` and `VAPID_PRIVATE_KEY` are not set, the server generates a new keypair on first start, prints both keys to stdout, and exits with a message asking to set them as environment variables. This is the intended workflow: run once to generate, then configure permanently.

## CLI

The binary supports one subcommand:

```
go-notify-server generate-vapid
```

Prints a fresh VAPID keypair to stdout:

```
VAPID_PUBLIC_KEY=BLkzGx5k3Rq...
VAPID_PRIVATE_KEY=dGhpcyBpcyB...
```

No other flags. The server starts with no subcommand:

```
go-notify-server
```

## Database Schema

Single SQLite database, two tables, created on startup if missing:

```sql
CREATE TABLE IF NOT EXISTS subscriptions (
    id         TEXT PRIMARY KEY,  -- random hex (16 bytes → 32 chars)
    topic      TEXT NOT NULL DEFAULT '',
    endpoint   TEXT NOT NULL,
    key_p256dh TEXT NOT NULL,
    key_auth   TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),

    UNIQUE(endpoint)  -- prevent duplicate subscriptions
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_topic ON subscriptions(topic);

CREATE TABLE IF NOT EXISTS delivery_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id TEXT NOT NULL,
    sent_at         TEXT NOT NULL DEFAULT (datetime('now')),
    status_code     INTEGER NOT NULL,  -- HTTP status from push service (201, 404, 410, etc.)
    error           TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_delivery_log_sent_at ON delivery_log(sent_at);
```

## API

All endpoints accept and return `application/json`.
Error responses use `{ "error": "message" }` with an appropriate HTTP status code.

### Public Endpoints

No authentication required. These are called by the Elm app.

---

#### `GET /vapid-public-key`

Returns the server's VAPID public key so the client can call `subscribePush`.

**Response:**

```json
{ "vapidPublicKey": "BLkzGx5k3Rq..." }
```

---

#### `POST /subscriptions`

Register a push subscription. The body matches the browser's `PushSubscription.toJSON()` format directly — no reshaping needed.

**Request body:**

```json
{
  "topic": "general",
  "subscription": {
    "endpoint": "https://fcm.googleapis.com/fcm/send/...",
    "expirationTime": null,
    "keys": {
      "p256dh": "BNcRdreALRF...",
      "auth": "tBHItJI5svk..."
    }
  }
}
```

- `topic` is optional (defaults to `""`). Topics allow sending notifications to subsets of subscribers (e.g., `"alerts"`, `"news"`, per-user IDs, etc.).
- `subscription` is required and must contain `endpoint` and `keys.p256dh` / `keys.auth`.
- `expirationTime` is accepted but ignored (not stored).

**Response (201 Created):**

```json
{ "id": "a1b2c3d4e5f6..." }
```

If the endpoint already exists, update the topic and keys, return the existing ID with `200 OK`.

---

#### `DELETE /subscriptions`

Unregister a push subscription by endpoint. Called by the Elm app when the user unsubscribes.

**Request body:**

```json
{
  "endpoint": "https://fcm.googleapis.com/fcm/send/..."
}
```

**Response (204 No Content):** empty body.

Returns `204` even if the endpoint was not found (idempotent).

---

### Admin Endpoints

Require `Authorization: Bearer <ADMIN_KEY>` header. Return `401` if missing or wrong.

---

#### `POST /notify`

Send a push notification.

**Request body:**

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

- `topic` is optional. If set, only subscriptions matching that topic are notified. If omitted or empty, all subscriptions are notified.
- `title` is required.
- `body`, `icon`, `badge`, `tag`, `url` are optional.

The server constructs the push payload as:

```json
{
  "title": "New message",
  "body": "You have a new message from Alice",
  "icon": "/icons/icon-192.png",
  "badge": "/icons/badge-72.png",
  "tag": "message-123",
  "data": { "url": "/messages/123" }
}
```

This matches the format the `elm-pwa` service worker expects in its `push` event handler. The `url` field from the request is nested under `data.url` in the payload.

**Delivery behavior:**

- Fan out to all matching subscriptions concurrently (bounded goroutine pool, e.g., 10 concurrent).
- For each subscription, POST the encrypted payload to the push service endpoint.
- If the push service returns `404` or `410` (Gone), delete the subscription from the database (it's stale).
- Log every delivery attempt to `delivery_log`.
- TTL: 24 hours (86400 seconds) for all messages.

**Response (200 OK):**

```json
{
  "sent": 42,
  "failed": 1,
  "stale_removed": 1
}
```

---

#### `GET /subscriptions`

List all subscriptions. For debugging and admin use.

**Query parameters:**

- `topic` (optional) — filter by topic

**Response (200 OK):**

```json
{
  "subscriptions": [
    {
      "id": "a1b2c3d4e5f6...",
      "topic": "general",
      "endpoint": "https://fcm.googleapis.com/fcm/send/...",
      "created_at": "2025-06-15T10:30:00Z"
    }
  ]
}
```

Keys (`p256dh`, `auth`) are not included in the list response — they're sensitive.

---

#### `DELETE /subscriptions/:id`

Remove a specific subscription by ID.

**Response (204 No Content):** empty body.

---

#### `DELETE /delivery-log`

Purge delivery log entries older than a given threshold.

**Query parameters:**

- `older_than` (optional, default `"30d"`) — duration string, e.g., `"7d"`, `"24h"`

**Response (200 OK):**

```json
{ "deleted": 1523 }
```

---

## CORS

All responses include:

```
Access-Control-Allow-Origin: <CORS_ORIGIN>
Access-Control-Allow-Methods: GET, POST, DELETE, OPTIONS
Access-Control-Allow-Headers: Content-Type, Authorization
```

All `OPTIONS` requests return `204` with these headers (preflight support).

## Middleware

Applied to every request in this order:

1. **CORS** — set headers, handle preflight
2. **Logging** — log `method path status duration` to stdout
3. **Auth check** — for admin endpoints only, verify bearer token
4. **Content-Type validation** — for POST/DELETE with body, require `application/json`

## Startup Sequence

1. Parse environment variables, validate required ones are set
2. Open SQLite database, run `CREATE TABLE IF NOT EXISTS` statements
3. Parse VAPID keys into `*ecdsa.PrivateKey` / `*ecdsa.PublicKey`
4. Start HTTP server, log `listening on :PORT`

## Graceful Shutdown

Listen for `SIGINT` / `SIGTERM`. On signal:

1. Stop accepting new connections (`http.Server.Shutdown` with 10s timeout)
2. Wait for in-flight notifications to complete
3. Close SQLite connection
4. Exit 0

## Dockerfile

```dockerfile
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /go-notify-server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /go-notify-server /usr/local/bin/go-notify-server
VOLUME /data
ENV DB_PATH=/data/notify.db
EXPOSE 8080
ENTRYPOINT ["go-notify-server"]
```

Use `CGO_ENABLED=0` since `modernc.org/sqlite` is pure Go.

The `/data` volume persists the SQLite database across container restarts.

## Project Structure

```
go-notify-server/
├── main.go              # entry point, CLI parsing, startup, shutdown
├── server.go            # HTTP handler setup, routing, middleware
├── handlers.go          # endpoint handlers
├── db.go                # SQLite operations (open, migrate, CRUD)
├── push.go              # web-push sending logic (fan-out, cleanup)
├── vapid.go             # VAPID key parsing and generation
├── Dockerfile
├── go.mod
├── go.sum
└── README.md
```

No `internal/` or `pkg/` directories — it's a small single-package application.

## Testing

#### From the Elm app

1. On startup, `GET /vapid-public-key` to retrieve the VAPID key
2. Pass it to `Pwa.subscribePush pwaOut vapidKey`
3. When `PushSubscription` event arrives, `POST /subscriptions` with the subscription JSON
4. On unsubscribe, `DELETE /subscriptions` with the endpoint

#### Sending a test notification

```sh
curl -X POST https://push.yourdomain.com/notify \
  -H "Authorization: Bearer your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{"title": "Test", "body": "Hello from go-notify-server", "url": "/"}'
```

#### Listing subscriptions

```sh
curl -H "Authorization: Bearer your-admin-key" \
  https://push.yourdomain.com/subscriptions
```
