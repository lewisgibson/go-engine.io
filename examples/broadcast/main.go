// Command broadcast runs a server that broadcasts every received message to all connected
// sessions, and emits a periodic server-originated broadcast as well.
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

	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		// Relay each message from one session to every session, chat-room style.
		// The server tracks the sessions, so there is no app-side registry to keep.
		socket.OnMessage(func(data []byte, isBinary bool) {
			broadcast(server, data, isBinary)
		})

		socket.OnClose(func(reason string, _ error) {
			fmt.Printf("disconnected: %s (%s)\n", socket.ID(), reason)
		})
	})

	// Emit a heartbeat broadcast to every session on a fixed cadence, showing a
	// server-originated fan-out rather than a relayed one.
	go func() {
		var ticker = time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return

			case t := <-ticker.C:
				broadcast(server, []byte("server time: "+t.Format(time.RFC3339)), false)
			}
		}
	}()

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
	fmt.Println("listening on :3000")

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}

// broadcast sends a message to every live session. Engine.IO has no built-in
// fan-out -- a ServerSocket only ever talks to its own client -- so an
// application broadcasts by iterating the server's sockets. Server.Sockets
// returns a snapshot, so the loop holds no lock while it sends. The sends are
// sequential and synchronous, though: a slow transport write delays the clients
// later in the loop, so a latency-sensitive server would fan the sends out across
// goroutines instead.
func broadcast(server *engineio.Server, data []byte, isBinary bool) {
	for _, socket := range server.Sockets() {
		if err := socket.Send(data, isBinary); err != nil {
			// A session that closed between the snapshot and the send rejects it
			// with ErrSocketClosed; that is expected during teardown, so log and
			// move on.
			fmt.Printf("broadcast to %s failed: %v\n", socket.ID(), err)
		}
	}
}
