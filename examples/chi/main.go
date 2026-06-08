// Command chi runs an Engine.IO echo server mounted on a go-chi v5 router. Every message
// a session sends is echoed straight back to that session.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-chi/chi/v5"
	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Create a server that echoes every message back to its sender.
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})

		socket.OnClose(func(reason string, _ error) {
			fmt.Printf("disconnected: %s (%s)\n", socket.ID(), reason)
		})
	})

	r := chi.NewRouter()

	// The Engine.IO server is already an http.Handler, so chi mounts it directly.
	// Chi's wildcard token is "*", so "/engine.io/*" matches every sub-path the
	// transport uses (handshake, polling, and the websocket upgrade).
	r.Handle("/engine.io/*", server)

	httpServer := &http.Server{
		Addr:              ":3000",
		Handler:           r,
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
