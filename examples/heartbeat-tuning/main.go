// Command heartbeat-tuning runs a server with a custom heartbeat. The server drives the v4
// heartbeat: it sends a ping every WithPingInterval and closes the session if a
// pong does not arrive within WithPingTimeout. The handshake advertises both
// values to the client, so a tuned server retunes its clients automatically.
package main

import (
	"context"
	"errors"
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

	// A short interval and timeout detect a dead connection quickly, at the cost
	// of more heartbeat traffic and less tolerance for a brief network stall or a
	// busy client. A long interval and timeout do the opposite: less traffic and
	// more tolerance, but a dead peer lingers until the timeout elapses. The
	// timeout should comfortably exceed the round-trip time so a healthy but slow
	// client is not closed mid-pong. The defaults are 25s interval / 20s timeout.
	server := engineio.NewServer(
		// Ping every 10s instead of 25s: faster liveness checks.
		engineio.WithPingInterval(10*time.Second),
		// Wait 5s for the pong instead of 20s: a dead peer is dropped sooner.
		engineio.WithPingTimeout(5*time.Second),
	)

	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})

		// A missed pong closes the session with reason "ping timeout"; logging the
		// reason here makes the heartbeat trade-off observable.
		socket.OnClose(func(reason string, _ error) {
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

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("server error: %v\n", err)
		}
	}()
	fmt.Println("listening on :3000 (ping every 10s, 5s pong timeout)")

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
