package engineio_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

// ExampleNewServer builds an Engine.IO server that echoes every message back to
// its sender and mounts it on the standard library's http.ServeMux. The server
// is a plain http.Handler, so it mounts in any router.
func ExampleNewServer() {
	server := engineio.NewServer(
		engineio.WithPingInterval(engineio.DefaultPingInterval),
		engineio.WithPingTimeout(engineio.DefaultPingTimeout),
		engineio.WithCORS(engineio.CORSOptions{
			AllowCredentials: true,
			AllowedOrigins:   []string{"https://example.com"},
		}),
	)

	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		// Echo every message back, preserving the binary flag.
		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})

		socket.OnClose(func(reason string, cause error) {
			fmt.Printf("disconnected: %s (%s)\n", socket.ID(), reason)
		})
	})

	mux := http.NewServeMux()
	mux.Handle("/engine.io/", server)

	// Mount mux on an http.Server and serve as usual; omitted here so the example
	// does not block.
	_ = &http.Server{Addr: ":3000", Handler: mux}
}

// ExampleNewSocket builds an Engine.IO client, registers its handlers, and
// opens it. Sends made before the socket opens are buffered and flushed once the
// handshake completes.
func ExampleNewSocket() {
	ctx := context.Background()

	client, err := engineio.NewSocket("http://localhost:3000/engine.io/",
		engineio.WithClient(&http.Client{Timeout: 30 * time.Second}),
		engineio.WithUpgrade(true),
		engineio.WithTransports(
			engineio.TransportTypePolling,
			engineio.TransportTypeWebSocket,
		),
	)
	if err != nil {
		panic(err)
	}

	client.OnOpen(func() {
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("Hello")},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})
	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("message (binary=%t): %s\n", isBinary, string(data))
	})
	client.OnPacket(func(packet engineio.Packet) {
		fmt.Printf("packet: %s\n", packet)
	})
	client.OnUpgrade(func(transportType engineio.TransportType) {
		fmt.Printf("upgraded to: %s\n", transportType)
	})
	client.OnError(func(err error) {
		fmt.Printf("error: %v\n", err)
	})
	client.OnClose(func(reason string, cause error) {
		fmt.Printf("close: %s (%v)\n", reason, cause)
	})

	// Open and close are omitted here so the example does not connect.
	_ = client
}

// ExampleEncodePacket round-trips a single packet through the text wire codec.
func ExampleEncodePacket() {
	packet := engineio.Packet{Type: engineio.PacketMessage, Data: []byte("hello")}

	encoded := engineio.EncodePacket(packet)

	decoded, err := engineio.DecodePacket(encoded)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s -> %s\n", encoded, decoded)
	// Output: 4hello -> Packet{Type: message, Data: hello}
}

// ExampleEncodePayload round-trips a long-polling payload of several packets.
// Encoding is v4-only; decoding takes the negotiated protocol version.
func ExampleEncodePayload() {
	packets := []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
		{Type: engineio.PacketMessage, Data: []byte("world")},
	}

	body := engineio.EncodePayload(packets)

	decoded, err := engineio.DecodePayload(engineio.ProtocolVersion4, body)
	if err != nil {
		panic(err)
	}

	fmt.Printf("decoded %d packets\n", len(decoded))
	// Output: decoded 2 packets
}
