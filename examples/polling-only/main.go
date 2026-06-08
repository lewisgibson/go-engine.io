// Command polling-only runs a client pinned to the long-polling transport. It offers only
// polling and disables upgrades, so the connection never switches to WebSocket.
// This is useful behind proxies or networks that do not pass WebSocket frames.
//
// To pin the SERVER to polling as well, construct it with:
//
//	engineio.NewServer(
//		engineio.WithServerTransports(engineio.TransportTypePolling),
//		engineio.WithAllowUpgrades(false),
//	)
//
// WithServerTransports limits which transports the server accepts, and
// WithAllowUpgrades(false) stops it advertising or accepting an upgrade.
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

	// Offer only polling and turn off upgrades: the socket will not probe for or
	// switch to WebSocket. WithTransports lists the transports to try, in order;
	// here that list is a single entry.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/",
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	if err != nil {
		panic(err)
	}

	client.OnOpen(func() {
		fmt.Println("open (transport: polling)")
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("hello over polling")},
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
