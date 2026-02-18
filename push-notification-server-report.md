# Web Push Notification Server: Options Report

## Background: How Web Push Actually Works

Before evaluating tools, it's important to understand the architecture. There are **three actors**:

1. **Browser** — calls `pushManager.subscribe()`, gets a `PushSubscription` with an endpoint URL on the browser vendor's push service (Google FCM for Chrome, Mozilla Autopush for Firefox, etc.)
2. **Browser vendor push service** — Google/Mozilla/Apple run these. You **cannot self-host** this part. The browser hardcodes which push service it uses.
3. **Application server** (what you'd build or deploy) — stores subscriptions, encrypts payloads (RFC 8291), signs with VAPID (RFC 8292), and POSTs to the vendor endpoints.

The encryption is **end-to-end**: Google/Mozilla cannot read your notification content. They only route encrypted blobs.

## Evaluated Solutions

### 1. Gotify — NOT SUITABLE

| | |
|---|---|
| **Website** | gotify.net |
| **Language** | Go + React |
| **Stars** | ~14,600 |
| **Maintained** | Yes (v2.9.0 Feb 2025) |

**Gotify uses WebSockets exclusively.** It does **not** implement RFC 8030 Web Push or VAPID. Notifications only arrive while the web UI tab is open (or a native companion app is running). There are open feature requests for Web Push ([#344](https://github.com/gotify/server/issues/344), open since 2020) but no implementation.

**Verdict:** Cannot send background push notifications to a PWA. Wrong protocol entirely.

### 2. ntfy.sh — PARTIALLY SUITABLE (with caveats)

| | |
|---|---|
| **Website** | ntfy.sh |
| **Language** | Go + React |
| **Stars** | ~28,800 |
| **Maintained** | Yes (v2.17.0 Feb 2026, daily commits) |

ntfy **does** implement Web Push (RFC 8030 + VAPID), using `webpush-go` internally. Its own PWA receives background push notifications even when the tab is closed.

**Critical limitation:** ntfy's Web Push is **tightly coupled to ntfy's own web app and service worker**. The Push API scopes subscriptions to the registering origin. This means:
- Your Elm PWA at `https://myapp.example.com` cannot receive push notifications from ntfy's Web Push subscriptions (those belong to `https://ntfy.example.com`)
- ntfy does not expose an API to accept arbitrary `PushSubscription` objects from external PWAs
- The ntfy author [has acknowledged this limitation](https://blog.ntfy.sh/2023/12/06/138-lines-of-code/)

**What ntfy CAN do:** If you're willing to use ntfy's own PWA as the notification UI (instead of your custom Elm app), it works excellently — lightweight, topic-based, simple REST API (`curl -d "message" ntfy.sh/topic`).

**Verdict:** Cannot serve as a Web Push backend for a **custom** PWA. Only works for ntfy's own web app.

### 3. Mercure — NOT SUITABLE

| | |
|---|---|
| **Website** | mercure.rocks |
| **Language** | Go (Caddy-based) |
| **Maintained** | Yes |

Mercure uses **Server-Sent Events (SSE)**, not Web Push. The [Mercure FAQ explicitly states](https://mercure.rocks/docs/spec/faq) that it and the Push API serve different purposes — Mercure is for live updates while the app is open, Push API is for notifications when the app is closed. They complement each other but don't replace each other.

**Verdict:** SSE requires an active connection. No background push notifications.

### 4. UnifiedPush — NOT SUITABLE (for browsers)

UnifiedPush is a decentralized push protocol primarily for **Android/Linux native apps**, designed as an alternative to Google FCM. It does NOT replace the browser's built-in push service. Web browsers always route push subscriptions through their vendor's service — UnifiedPush is irrelevant for browser-based Web Push.

### 5. Other Evaluated Options

| Solution | Why not suitable |
|---|---|
| **Pushover** | Commercial, hosted-only, proprietary protocol, not self-hostable |
| **Apprise** | Notification aggregator (fans out to 100+ services). Does not implement RFC 8030. |
| **Novu** | Heavyweight enterprise platform (MongoDB, Redis, microservices). Delegates push to FCM/OneSignal. |
| **Courier** | Commercial, hosted-only |
| **Mozilla Autopush-rs** | Implements the **push service** role (what Google/Mozilla run), not the application server role. Designed for Mozilla-scale infrastructure (DynamoDB, millions of connections). Wrong layer. |
| **Perfecty Push** | Go server that does implement RFC 8030, but appears abandoned (~20 commits, last activity ~2021). No topic support. Not production-ready. |
| **Various Node.js servers** | Small demo projects, all abandoned/unmaintained. |

## Summary Comparison

| Solution | RFC 8030 Web Push? | Self-hosted? | Lightweight? | Maintained? | Works with custom PWA? | Topics? |
|---|---|---|---|---|---|---|
| Gotify | No (WebSocket) | Yes | Yes | Yes | No | No |
| **ntfy.sh** | Yes, but own PWA only | Yes | Yes | Yes | **No** | Yes |
| Mercure | No (SSE) | Yes | Yes | Yes | No | Yes |
| UnifiedPush | For native apps only | Yes | Yes | Yes | No | N/A |
| Perfecty Push | Yes | Yes | Yes | **No (abandoned)** | Yes | No |
| **Custom server + webpush-go** | **Yes** | **Yes** | **Yes** | **Yes** | **Yes** | **Yes** |

## Recommendation

**There is no off-the-shelf, lightweight, self-hosted server that implements RFC 8030 Web Push with subscription management and per-topic fan-out for custom PWAs.**

The reason is structural: the "application server" role in Web Push is intentionally thin and application-specific (your subscription schema, your topic model, your auth). The ecosystem provides **libraries** for the hard cryptographic parts, not turnkey servers.

### Best option: Build a custom Go server using webpush-go

**[SherClockHolmes/webpush-go](https://github.com/SherClockHolmes/webpush-go)** (414 stars, v1.4.0 Jan 2025, MIT, actively maintained, packaged in Debian) is the de facto standard Go library. It handles:
- VAPID key generation
- RFC 8291 payload encryption
- RFC 8292 VAPID signing
- Sending to browser push endpoints

Your custom server wraps this with:
- HTTP endpoints (accept subscriptions, trigger notifications)
- SQLite storage (subscriptions tagged by topic)
- Admin auth (bearer token)
- Stale subscription cleanup (delete on 404/410)

This is exactly what the `go-notify-server.md` spec describes. The server is roughly 500–1000 lines of Go. The browser vendors' push services handle all the hard infrastructure (persistent connections to millions of browsers, wake-on-push, battery optimization). Your server just encrypts and POSTs.

### Alternative: Use ntfy as-is (if you can adapt)

If you're flexible about using ntfy's own web app instead of a custom Elm PWA for the notification UI, ntfy is excellent — single binary, topic-based, trivial API, very actively maintained. But this means giving up the custom PWA experience.

## Bottom line

The spec in `go-notify-server.md` is the right approach. No existing tool fills this exact niche, and building it is straightforward with `webpush-go`.
