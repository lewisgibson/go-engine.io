// Command client runs an Engine.IO client against a server on localhost:3000. It connects,
// greets the server once the connection opens, echoes back anything the server
// sends, and exits when the connection closes or the process is interrupted. It
// demonstrates the full client lifecycle: configure, register handlers, open,
// and close.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// closed is signalled by the close handler so main can return once the
	// connection ends.
	closed := make(chan struct{}, 1)

	// Configure the client: a custom HTTP client and headers, and an upgrade from
	// long-polling to websocket that is remembered for the next connection.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/",
		engineio.WithClient(&http.Client{
			Timeout: 30 * time.Second,
		}),
		engineio.WithHeader(http.Header{
			"Authorization": []string{"Bearer token"},
		}),
		engineio.WithUpgrade(true),
		engineio.WithRememberUpgrade(true),
		engineio.WithTransports(
			engineio.TransportTypePolling,
			engineio.TransportTypeWebSocket,
		),
	)
	if err != nil {
		panic(err)
	}

	// Echo every message the server sends straight back, preserving whether it was
	// binary. The transport upgrade, if it happens, is transparent here.
	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("message from server (binary=%t): %s\n", isBinary, string(data))
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: data, IsBinary: isBinary},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})

	// Once the handshake completes, greet the server. A Send issued after Open() but
	// before the handshake completes is buffered and flushed here; a Send made before
	// Open() (while the socket is still closed) is silently dropped.
	client.OnOpen(func() {
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("Hello")},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})

	// Report the close reason and cause, then release main.
	client.OnClose(func(reason string, cause error) {
		fmt.Printf("close: %v, %v\n", reason, cause)
		closed <- struct{}{}
	})

	// Surface transport errors; an error is not necessarily fatal.
	client.OnError(func(err error) {
		fmt.Printf("error: %v\n", err)
	})

	// Open the connection and ensure it is closed on the way out.
	client.Open(ctx)
	defer client.Close(ctx)

	// Wait until the connection closes or the process is interrupted.
	select {
	case <-closed:

	case <-ctx.Done():
	}
}
