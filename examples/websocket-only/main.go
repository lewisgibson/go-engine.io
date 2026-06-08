// Command websocket-only runs a client pinned to the WebSocket transport. It offers only
// WebSocket, so there is no polling phase and no upgrade: the handshake happens
// directly over the WebSocket connection. This skips the polling round-trips
// when the network is known to pass WebSocket frames.
//
// To pin the SERVER to WebSocket as well, construct it with:
//
//	engineio.NewServer(
//		engineio.WithServerTransports(engineio.TransportTypeWebSocket),
//		engineio.WithAllowUpgrades(false),
//	)
//
// WithServerTransports limits which transports the server accepts, and
// WithAllowUpgrades(false) stops it advertising or accepting an upgrade. With
// WebSocket as the only transport there is nothing to upgrade from.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// closed is signalled by the close handler so main can return once the
	// connection ends.
	closed := make(chan struct{}, 1)

	// Offer only WebSocket and turn off upgrades. With a single transport there is
	// no fallback, so a network that blocks WebSocket frames fails to connect
	// rather than silently degrading to polling.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/",
		engineio.WithTransports(engineio.TransportTypeWebSocket),
		engineio.WithUpgrade(false),
	)
	if err != nil {
		panic(err)
	}

	client.OnOpen(func() {
		fmt.Println("open (transport: websocket)")
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("hello over websocket")},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})

	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("message (binary=%t): %s\n", isBinary, string(data))
	})

	client.OnClose(func(reason string, cause error) {
		fmt.Printf("close: %s (%v)\n", reason, cause)
		closed <- struct{}{}
	})

	client.OnError(func(err error) {
		fmt.Printf("error: %v\n", err)
	})

	client.Open(ctx)
	defer client.Close(ctx)

	// Wait until the connection closes or the process is interrupted.
	select {
	case <-closed:

	case <-ctx.Done():
	}
}
