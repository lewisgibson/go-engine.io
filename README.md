# go-engine.io

[![Build Workflow](https://github.com/lewisgibson/go-engine.io/actions/workflows/build.yaml/badge.svg)](https://github.com/lewisgibson/go-engine.io/actions/workflows/build.yaml)
[![codecov](https://codecov.io/gh/lewisgibson/go-engine.io/graph/badge.svg)](https://codecov.io/gh/lewisgibson/go-engine.io)
[![Pkg Go Dev](https://pkg.go.dev/badge/github.com/lewisgibson/go-engine.io)](https://pkg.go.dev/github.com/lewisgibson/go-engine.io)

A Go implementation of the [Engine.IO](https://socket.io/docs/v4/engine-io-protocol/) v4 protocol: the transport layer that Socket.IO is built on. Engine.IO provides a reliable, bidirectional connection between a client and a server using HTTP long-polling and WebSocket, with an automatic upgrade from long-polling to WebSocket once a session is established. This package ships a client (`Socket`), a server (`Server`, an `http.Handler`), and a version-aware packet/payload codec.

## Features

- ✅ **Client**: A `Socket` that performs the handshake, runs the heartbeat, and buffers writes across an upgrade
- ✅ **Server**: A `Server` that is a plain `http.Handler`, mountable in any router
- ✅ **HTTP Long-Polling Transport**: The baseline transport that works everywhere
- ✅ **WebSocket Transport**: A full-duplex transport for low-latency messaging
- ✅ **Automatic Transport Upgrade**: Background probe that upgrades long-polling to WebSocket
- ✅ **Binary Support**: Round-trip binary messages without downgrading them to text
- ✅ **Version-Aware Decoding**: Decodes v2, v3, and v4 long-polling payload framings
- ✅ **Heartbeat**: Server-initiated v4 ping/pong with timeout detection
- ✅ **CORS**: Configurable cross-origin policy on the server
- ✅ **Concurrent Safe**: All client and server socket methods are safe for concurrent use
- ✅ **Standard Library HTTP**: Built on `net/http`; works with Echo, Gin, chi, and more

## Resources

- [Discussions](https://github.com/lewisgibson/go-engine.io/discussions)
- [Reference](https://pkg.go.dev/github.com/lewisgibson/go-engine.io)
- [Examples](https://github.com/lewisgibson/go-engine.io/tree/main/examples)

## Installation

```sh
go get github.com/lewisgibson/go-engine.io
```

## Quickstart

### Client

Create a `Socket`, register handlers, then `Open` it. Sends made after `Open()`
but before the handshake completes are buffered and flushed once the socket opens;
a `Send` before `Open()` (or after close) is silently dropped. All options are
optional; `NewSocket(url)` works out of the box with the defaults in the table
below.

```go
package main

import (
	"context"
	"fmt"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx := context.Background()

	// NewSocket(url) works out of the box. Every option is optional (see the table
	// below); by default the socket starts on long-polling and upgrades to
	// WebSocket.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/")
	if err != nil {
		panic(err)
	}

	// Invoked once the handshake completes. You need not wait for it to send -- a
	// Send any time after Open() is buffered and flushed here.
	client.OnOpen(func() {
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("Hello")},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})

	// Invoked for each application message. isBinary reports whether the peer
	// sent it as a binary frame.
	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("message (binary=%t): %s\n", isBinary, string(data))
	})

	// Invoked for every packet, including protocol packets such as ping and open.
	client.OnPacket(func(packet engineio.Packet) {
		fmt.Printf("packet: %s\n", packet)
	})

	// Invoked once the socket finishes upgrading to a better transport.
	client.OnUpgrade(func(transportType engineio.TransportType) {
		fmt.Printf("upgraded to: %s\n", transportType)
	})

	// Invoked on a transport error; an error is not always fatal.
	client.OnError(func(err error) {
		fmt.Printf("error: %v\n", err)
	})

	// Invoked once when the socket closes. cause is the error if one occurred,
	// else nil (a clean close and a ping timeout both pass nil; branch on reason).
	client.OnClose(func(reason string, cause error) {
		fmt.Printf("close: %s (%v)\n", reason, cause)
	})

	// Open the connection, then close it when done.
	client.Open(ctx)
	defer client.Close(ctx)
}
```

The client handlers are:

| Handler                                       | Fires when                                                                                                                |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `OnOpen(func())`                              | The handshake completes and the socket is ready                                                                           |
| `OnMessage(func(data []byte, isBinary bool))` | An application message arrives                                                                                            |
| `OnPacket(func(Packet))`                      | Any packet arrives, including protocol packets                                                                            |
| `OnUpgrade(func(TransportType))`              | The socket switches to a better transport                                                                                 |
| `OnUpgradeError(func(error))`                 | An upgrade probe fails (non-fatal; stays on the current transport)                                                        |
| `OnError(func(error))`                        | A transport error occurs (not always fatal); also receives upgrade-probe failures when no `OnUpgradeError` handler is set |
| `OnClose(func(reason string, cause error))`   | The socket closes                                                                                                         |

The client options, with their defaults:

| Option                             | Default                     | Purpose                                           |
| ---------------------------------- | --------------------------- | ------------------------------------------------- |
| `WithClient(TransportClient)`      | `&http.Client{}`            | HTTP client used by the transports                |
| `WithHeader(http.Header)`          | empty `http.Header`         | Headers sent on every transport request           |
| `WithUpgrade(bool)`                | `true`                      | Try to upgrade long-polling to a better transport |
| `WithRememberUpgrade(bool)`        | `false`                     | Reuse a prior successful upgrade on the next open |
| `WithTransports(...TransportType)` | `polling`, then `websocket` | Transports to try, in order                       |
| `WithTryAllTransports(bool)`       | `false`                     | Try every transport in the list before giving up  |

See [`examples/client`](examples/client/main.go) for a complete program.

### Server

The server is an `http.Handler`. Mount it at the Engine.IO path (typically
`/engine.io/`) and configure each session in the connection handler.

All server options are optional; `NewServer()` works out of the box with the
defaults in the table below. Configure only what you need.

```go
package main

import (
	"fmt"
	"net/http"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	// Every option is optional. This passes one or two for illustration; pass
	// none to accept the defaults.
	server := engineio.NewServer(
		engineio.WithPingInterval(20 * time.Second),
		engineio.WithCORS(engineio.CORSOptions{
			AllowedOrigins: []string{"https://example.com"},
		}),
	)

	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		// Echo every message back to its sender, preserving the binary flag.
		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})

		// cause is the error if one occurred, else nil (branch on reason, not cause).
		socket.OnClose(func(reason string, cause error) {
			fmt.Printf("disconnected: %s (%s)\n", socket.ID(), reason)
		})
	})

	mux := http.NewServeMux()
	mux.Handle("/engine.io/", server)

	httpServer := &http.Server{
		Addr:              ":3000",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		fmt.Printf("server error: %v\n", err)
	}
}
```

`ServerSocket` is the per-session handle passed to the connection handler:

- `OnMessage(func(data []byte, isBinary bool))` registers the message handler.
- `OnClose(func(reason string, cause error))` registers the close handler.
- `Send(data []byte, isBinary bool) error` queues a message; pass `true` to send
  it as binary. It returns `ErrSocketClosed` once the session is closing or closed
  -- from the moment `Close()` (or a teardown) begins, which may be before the
  close handler fires.
- `ID() string` returns the session identifier from the handshake.
- `Close() error` gracefully closes the session, delivering a close packet first.

`Server.Close()` tears down every live session with reason `"forced close"`,
firing each socket's close handler; it does not stop the underlying
`http.Server`, which the caller shuts down separately.

For fan-out, the server exposes its live sessions directly, so an application
never has to keep its own registry:

- `Sockets() []*ServerSocket` returns a snapshot of every live session, safe to
  range over and send on. It is the building block for broadcast, since a
  `ServerSocket` only ever talks to its own client.
- `Count() int` returns the number of live sessions.
- `Socket(id string) (*ServerSocket, bool)` looks a session up by its id; the
  bool reports whether a live session with that id exists.

These mirror the reference engine.io server's `clients` registry and
`clientsCount`. See [`examples/broadcast`](examples/broadcast/main.go) for a
chat-style relay built on `Sockets()`.

The server options, with their defaults:

| Option                                          | Default                            | Purpose                                                                                                       |
| ----------------------------------------------- | ---------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `WithPingInterval(time.Duration)`               | `DefaultPingInterval` (25s)        | How often the server pings                                                                                    |
| `WithPingTimeout(time.Duration)`                | `DefaultPingTimeout` (20s)         | How long to wait for a pong before closing                                                                    |
| `WithUpgradeTimeout(time.Duration)`             | `DefaultUpgradeTimeout` (10s)      | How long an upgrade probe may take                                                                            |
| `WithMaxPayload(int)`                           | `DefaultMaxPayload` (1000000)      | Maximum accepted POST body size, in bytes                                                                     |
| `WithServerTransports(...TransportType)`        | `polling`, `websocket`             | Transports the server accepts                                                                                 |
| `WithAllowUpgrades(bool)`                       | `true`                             | Advertise and accept transport upgrades                                                                       |
| `WithCORS(CORSOptions)`                         | allow all origins                  | Cross-origin policy (see below)                                                                               |
| `WithGenerateID(func(r *http.Request) string)`  | 18 random bytes, base64url-encoded | Session identifier generator; receives the handshake request, so the id can be derived from a header or token |
| `WithAllowRequest(func(r *http.Request) error)` | unset (allow all)                  | Gate each handshake (auth, tokens, rate limit); a non-nil error rejects it with 403                           |
| `WithCookie(CookieOptions)`                     | no cookie                          | Session-affinity cookie for sticky sessions behind a load balancer                                            |
| `WithHTTPCompression(bool)`                     | `true`                             | gzip long-poll responses above a threshold when the client advertises it                                      |

The server also exposes
`OnConnectionError(func(r *http.Request, code ConnectionErrorCode, reason string))`,
invoked when a handshake is rejected by validation before a session is established
(a failed allow-request gate, an unsupported protocol version, an unknown or
disallowed transport, a bad handshake method, or an unknown polling session id).
It is best-effort: a few low-level failures bypass it (an unknown-sid WebSocket
upgrade, or a failure to build or send the open packet). `code` is one of the
exported `ConnectionError*` constants
(`ConnectionErrorUnknownTransport`, `ConnectionErrorUnknownSessionID`,
`ConnectionErrorBadHandshakeMethod`, `ConnectionErrorBadRequest`,
`ConnectionErrorForbidden`, `ConnectionErrorUnsupportedProtocolVersion`), so a
handler can branch on why the connection was refused.

`CORSOptions` controls the cross-origin headers the server sets:

```go
type CORSOptions struct {
	// AllowCredentials sets Access-Control-Allow-Credentials. When true, the
	// server echoes the request origin instead of replying with "*".
	AllowCredentials bool
	// AllowedOrigins is the set of origins permitted to connect. An empty slice,
	// or a single "*" entry, allows every origin.
	AllowedOrigins []string
	// AllowedHeaders is advertised in the preflight response. An empty slice
	// advertises Content-Type.
	AllowedHeaders []string
}
```

See [`examples/server`](examples/server/main.go) for a complete program with
graceful shutdown.

### Web frameworks

Because the server is a plain `http.Handler`, it mounts in any router. Complete
examples live in [`examples/`](examples):

```go
// net/http
mux.Handle("/engine.io/", server)

// Echo (github.com/labstack/echo/v4)
e.Any("/engine.io/*", echo.WrapHandler(server))

// Gin (github.com/gin-gonic/gin)
r.Any("/engine.io/*any", gin.WrapH(server))

// chi (github.com/go-chi/chi/v5)
r.Handle("/engine.io/*", server)
```

## Transports

Engine.IO defines two transports, both implemented here:

- **HTTP long-polling** (`TransportTypePolling`): the baseline. The client issues
  a long-lived `GET` that the server holds open until it has data to deliver, and
  posts outbound packets with `POST`. It works in every environment, including
  behind restrictive proxies, but carries the latency of repeated requests.
- **WebSocket** (`TransportTypeWebSocket`): a full-duplex connection that carries
  packets as individual frames with no per-message HTTP overhead, including native
  binary frames.

A session starts on long-polling and, with upgrades enabled, the client probes
for WebSocket in the background: it opens a second transport, sends a `ping`
with the `"probe"` payload, and on the matching `pong` commits the switch with an
`upgrade` packet. Writes are buffered across the switch so nothing is lost or
reordered. Upgrades are on by default; disable them with `WithUpgrade(false)` on
the client or `WithAllowUpgrades(false)` on the server.

## Compatibility

This library always speaks Engine.IO protocol v4 on the wire (the `Protocol`
constant, sent as the `EIO` query parameter). Payload **decoding** is
version-aware so the codec can read messages produced by older peers, but
**encoding** is v4-only.

| Concern          | v2 (`ProtocolVersion2`) | v3 (`ProtocolVersion3`)    | v4 (`ProtocolVersion4`)     |
| ---------------- | ----------------------- | -------------------------- | --------------------------- |
| Payload decoding | ✅ string framing       | ✅ string + binary framing | ✅ record-separator framing |
| Payload encoding | ❌                      | ❌                         | ✅                          |
| Server (`EIO`)   | ❌                      | ❌                         | ✅ only                     |
| Client (`EIO`)   | ❌                      | ❌                         | ✅ only                     |

The server rejects any handshake whose `EIO` is not `4`; the client always sends
`EIO=4`.

Interoperability with the reference JavaScript implementation is verified by the
build-tagged tests in [`test/interop`](test/interop): the Go client is driven
against the canonical `engine.io` server, and the Go server against the canonical
`engine.io-client`, exercising the handshake, long-polling, the WebSocket
upgrade, binary messages, and the heartbeat. Run them with Node.js installed:

```sh
make interop
```

## Codec

The packet and payload codec is exported for direct use. A `Packet` is a single
protocol packet; a payload is a long-polling framing of one or more packets.

A `Packet` carries a type, its data, and a flag marking binary messages:

```go
type Packet struct {
	// Type is the packet kind (open, close, ping, pong, message, upgrade, noop).
	Type PacketType
	// Data is the payload: UTF-8 bytes for a text packet, raw bytes for a binary
	// message (already base64-decoded).
	Data []byte
	// IsBinary reports whether Data is a binary message. Only a message may be
	// binary, so IsBinary == true implies Type == PacketMessage.
	IsBinary bool
}
```

The seven packet types are `PacketOpen`, `PacketClose`, `PacketPing`,
`PacketPong`, `PacketMessage`, `PacketUpgrade`, and `PacketNoop`.

Encode and decode a single packet:

```go
packet := engineio.Packet{Type: engineio.PacketMessage, Data: []byte("hello")}

// EncodePacket returns the text wire form ("4hello").
encoded := engineio.EncodePacket(packet)

// DecodePacket parses it back into a Packet.
decoded, err := engineio.DecodePacket(encoded)
if err != nil {
	panic(err)
}
fmt.Printf("%s\n", decoded) // Packet{Type: message, Data: hello}
```

Encode and decode a long-polling payload of several packets. Encoding is v4-only;
decoding takes the negotiated `ProtocolVersion` so it can read v2/v3 framings too:

```go
packets := []engineio.Packet{
	{Type: engineio.PacketMessage, Data: []byte("hello")},
	{Type: engineio.PacketMessage, Data: []byte("world")},
}

// EncodePayload joins the packets with the v4 record separator.
body := engineio.EncodePayload(packets)

// DecodePayload frames the body back into packets for the given version.
decoded, err := engineio.DecodePayload(engineio.ProtocolVersion4, body)
if err != nil {
	panic(err)
}
fmt.Printf("decoded %d packets\n", len(decoded))
```

## API Reference

### Core Types

- `Socket` - the Engine.IO v4 client connection; created with `NewSocket`.
- `Server` - the Engine.IO v4 server (`http.Handler`); created with `NewServer`.
- `ServerSocket` - a single connected server session, handed to `OnConnection`.
- `Transport` / `TransportType` - the transport interface and its kinds
  (`TransportTypePolling`, `TransportTypeWebSocket`).
- `Packet` / `PacketType` - a protocol packet and its type constants.
- `OpenPacket` - the JSON payload of the handshake `open` packet.
- `CORSOptions` - the server's cross-origin configuration.
- `ProtocolVersion` - an Engine.IO version (`ProtocolVersion2/3/4`); `Protocol`
  is the version this library speaks (v4).

### Constructors

- `NewSocket(serverURL string, options ...SocketOption) (*Socket, error)` - build a client.
- `NewServer(options ...ServerOption) *Server` - build a server.

### Codec Functions

- `EncodePacket(packet Packet) []byte` - encode one packet to its text wire form.
- `DecodePacket(input []byte) (Packet, error)` - decode one packet.
- `EncodePayload(packets []Packet) []byte` - encode packets to a v4 payload.
- `DecodePayload(version ProtocolVersion, input []byte) ([]Packet, error)` -
  decode a payload for the given protocol version (v2/v3/v4).

### Sentinel Errors

- `ErrInvalidURL` - the server URL could not be parsed (`NewSocket`).
- `ErrNoTransports` - no transports are available to open (`Socket.Open`).
- `ErrSocketClosed` - a send was attempted on a closed `ServerSocket`.
- `ErrEmptyPacket` - the codec was given empty input.
- `ErrInvalidPacketType` - the codec saw an unknown packet type byte.
- `ErrMalformedPayload` - a payload could not be framed.
- `ErrUnsupportedProtocolVersion` - decoding was requested for a version the
  codec does not understand.
- `ErrURLRequired` - a transport constructor was called with a nil URL.
- `ErrTransportRoundTripperClientRequired` - a `TransportRoundTripper` was used
  without a `Client`.
- `ErrUnexpectedStatus` - a polling transport request returned a non-200 HTTP
  status.

## Performance

The codec, the server send path, and the version-aware decoders have benchmarks.
Run them with:

```sh
go test -run '^$' -bench . -benchmem .
```

Indicative results on an AMD Ryzen 9 9950X3D (`linux/amd64`, Go 1.26); your
numbers will differ:

| Benchmark                             | ns/op | B/op | allocs/op |
| ------------------------------------- | ----: | ---- | --------: |
| `EncodePacket` (small text)           |    23 | 24   |         2 |
| `DecodePacket` (small text)           |    77 | 0    |         0 |
| `EncodePacket` (binary)               |   203 | 360  |         2 |
| `DecodePacket` (binary)               |   323 | 640  |         2 |
| `EncodePayload` (small)               |    45 | 56   |         3 |
| `EncodePayload` (large batch)         |   698 | 1912 |         8 |
| `DecodePayload` (v4 record separator) |   216 | 264  |         3 |
| `DecodePayload` (v3 binary framing)   |   154 | 136  |         3 |
| `DecodePayload` (v2 string framing)   |   203 | 144  |         4 |
| `Server.Send` (text)                  | 7,900 | 7209 |        33 |
| `Server` poll round trip              | 9,900 | 7208 |        33 |

## Examples

The [examples directory](examples/) has complete, runnable programs (see its
[README](examples/README.md) for the full index), including:

- Basic [client](examples/client/main.go) and [server](examples/server/main.go).
- Deriving a session id from a request token: [auth-session-id](examples/auth-session-id/main.go).
- [broadcast](examples/broadcast/main.go) to every session, [binary](examples/binary/main.go) messaging, and [CORS](examples/cors/main.go).
- [heartbeat-tuning](examples/heartbeat-tuning/main.go), [graceful-shutdown](examples/graceful-shutdown/main.go), and a custom [net-http](examples/net-http/main.go) mount.
- Single-transport clients ([polling-only](examples/polling-only/main.go), [websocket-only](examples/websocket-only/main.go)) and one wiring [every client event](examples/client-events/main.go).
- The server mounted on [Echo](examples/echo/main.go), [Gin](examples/gin/main.go), and [chi](examples/chi/main.go).

## License

This project is licensed under the MIT License - see the [LICENSE.md](LICENSE.md)
file for details.
