# Examples

Runnable, self-contained examples for `github.com/lewisgibson/go-engine.io`.

This directory is its own Go module with a `replace` directive pointing at the
parent, so the examples always build against the local source. Each example is
`package main` in its own folder with a single `func main()`. Most servers listen
on `:3000` and mount Engine.IO at `/engine.io/`; the clients connect to
`http://localhost:3000/engine.io/`.

Run one with, e.g.:

```sh
go run ./echo
```

Pair a server example with a client example by running each in its own terminal.

## Servers

- [server/](server/) -- echo server on the standard library's `http.ServeMux`.
- [net-http/](net-http/) -- mounting the server at a custom path on a `net/http` mux, alongside ordinary routes.
- [chi/](chi/) -- echo server mounted on a go-chi v5 router.
- [echo/](echo/) -- echo server mounted on a labstack Echo v4 router.
- [gin/](gin/) -- echo server mounted on a Gin router.
- [auth-session-id/](auth-session-id/) -- deriving the session id from the request via `WithGenerateID(func(r *http.Request) string)`, resolving an `Authorization` header to a user.
- [allow-request/](allow-request/) -- gating handshakes with `WithAllowRequest` (auth/token/rate-limit) and logging rejections with `OnConnectionError`.
- [broadcast/](broadcast/) -- broadcasting to every session by iterating `Server.Sockets()` (engine.io has no built-in broadcast).
- [cors/](cors/) -- configuring cross-origin access for a browser with `WithCORS`.
- [sticky-sessions/](sticky-sessions/) -- setting the session-affinity cookie with `WithCookie` to scale long-polling horizontally behind a load balancer.
- [heartbeat-tuning/](heartbeat-tuning/) -- custom `WithPingInterval` / `WithPingTimeout` and the trade-offs.
- [graceful-shutdown/](graceful-shutdown/) -- closing all sessions and draining the HTTP server on SIGINT/SIGTERM.

## Clients

- [client/](client/) -- the full client lifecycle: configure, register handlers, open, and close.
- [client-events/](client-events/) -- all seven client handlers (`OnOpen`, `OnMessage`, `OnPacket`, `OnUpgrade`, `OnUpgradeError`, `OnError`, `OnClose`) with explanations.
- [polling-only/](polling-only/) -- a client pinned to long-polling (and the matching server option).
- [websocket-only/](websocket-only/) -- a client pinned to WebSocket (and the matching server option).

## Server and client together

- [binary/](binary/) -- a server and a client in one process exchanging binary messages with the `isBinary` flag preserved.
- [webtransport/](webtransport/) -- a server and a client in one process talking over the WebTransport (HTTP/3) transport, with a throwaway self-signed certificate.
