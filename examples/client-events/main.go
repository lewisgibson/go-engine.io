// Command client-events runs a client that wires up all seven client event
// handlers, each with a comment explaining when it fires and what it is for. The
// handlers run on transport goroutines and must not block.
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

	// Allow an upgrade so OnUpgrade has a chance to fire: the socket starts on
	// polling and probes for WebSocket in the background.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/",
		engineio.WithUpgrade(true),
		engineio.WithTransports(
			engineio.TransportTypePolling,
			engineio.TransportTypeWebSocket,
		),
	)
	if err != nil {
		panic(err)
	}

	// 1. OnOpen fires once, after the handshake completes. It is the first point at
	// which a send is guaranteed to flush. A send issued after Open() while the
	// socket is still opening is buffered and flushed here; a send made before
	// Open() (while the socket is closed) is silently dropped.
	client.OnOpen(func() {
		fmt.Println("OnOpen: handshake complete, socket is open")
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("hello")},
		}); err != nil {
			fmt.Printf("send error: %v\n", err)
		}
	})

	// 2. OnMessage fires for each application message. The isBinary flag reports
	// whether the peer sent the payload as a binary frame, so binary round-trips
	// without being downgraded to text. This is the handler most applications use.
	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("OnMessage (binary=%t): %s\n", isBinary, string(data))
	})

	// 3. OnPacket fires for EVERY packet, including protocol packets such as ping
	// and open, in arrival order. Use it to observe the raw protocol; most
	// applications prefer OnMessage, which sees only application data.
	client.OnPacket(func(packet engineio.Packet) {
		fmt.Printf("OnPacket: %s\n", packet)
	})

	// 4. OnUpgrade fires once the socket finishes switching to a better transport,
	// reporting the type it moved to (here, websocket). It fires after the switch
	// is committed and before buffered writes flush over the new transport.
	client.OnUpgrade(func(transportType engineio.TransportType) {
		fmt.Printf("OnUpgrade: now using %s\n", transportType)
	})

	// 5. OnUpgradeError fires when an upgrade probe fails. A failed probe is
	// non-fatal: the socket keeps running on its current transport, so it is
	// distinct from OnError and lets you detect, e.g. a proxy that blocks
	// WebSocket. When no OnUpgradeError handler is set, the probe failure is
	// reported to OnError instead.
	client.OnUpgradeError(func(err error) {
		fmt.Printf("OnUpgradeError: %v\n", err)
	})

	// 6. OnError fires on a transport failure or a malformed packet. An error is
	// not always fatal, so do not treat every error as a close; a failed upgrade
	// probe is delivered to OnUpgradeError above, not here, when that handler is
	// set.
	client.OnError(func(err error) {
		fmt.Printf("OnError: %v\n", err)
	})

	// 7. OnClose fires once when the socket closes. The reason is a short
	// description and the cause is the underlying error (nil for a graceful
	// close), so the application can log or branch on why the connection ended.
	client.OnClose(func(reason string, cause error) {
		fmt.Printf("OnClose: reason=%q cause=%v\n", reason, cause)
		closed <- struct{}{}
	})

	client.Open(ctx)
	defer client.Close(ctx)

	// Wait until the connection closes or the process is interrupted.
	select {
	case <-closed:

	case <-ctx.Done():
	}
}
