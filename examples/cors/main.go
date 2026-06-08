// Command cors runs a server configured for cross-origin access from a browser served on
// a different origin. With WithCORS the server answers preflight requests and
// sets the Access-Control-* headers so a browser at the named origin can connect.
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

	// Restrict access to a single browser origin and allow credentials so the
	// browser may send cookies. Because AllowedOrigins names a concrete origin, the
	// server echoes that origin in Access-Control-Allow-Origin when the request
	// matches (it never replies "*" here); AllowCredentials additionally sets
	// Access-Control-Allow-Credentials: true. The echo-instead-of-"*" rule only
	// matters for an allow-all policy: an empty AllowedOrigins (or a single "*")
	// allows every origin.
	server := engineio.NewServer(
		engineio.WithCORS(engineio.CORSOptions{
			AllowCredentials: true,
			AllowedOrigins:   []string{"http://localhost:5173"},
			AllowedHeaders:   []string{"Content-Type", "Authorization"},
		}),
	)

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
	fmt.Println("listening on :3000 (allowing origin http://localhost:5173)")

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
