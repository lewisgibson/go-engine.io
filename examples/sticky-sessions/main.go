// Command sticky-sessions runs an Engine.IO server that sets a session-affinity
// cookie, the mechanism Engine.IO uses to scale horizontally behind a load
// balancer.
//
// HTTP long-polling makes several requests per session, and they must all reach
// the node holding that session's state. WithCookie writes a cookie carrying the
// session id on the handshake response; a sticky (cookie-aware) load balancer
// then pins every later request for that session to the same node. The cookie is
// set once, at session creation. A WebSocket-only deployment does not need it (a
// single upgraded connection stays on one node), but a long-polling or
// polling-then-upgrade deployment spread across nodes does.
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

	// Set the affinity cookie that carries the session id. HttpOnly and
	// SameSite=Lax are sensible defaults; add Secure (and SameSite=None) when
	// serving over HTTPS across origins. The cookie name and path must match what
	// the load balancer is configured to pin on.
	server := engineio.NewServer(
		engineio.WithCookie(engineio.CookieOptions{
			Name:     "io",
			Path:     "/",
			HTTPOnly: true,
			SameSite: http.SameSiteLaxMode,
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
	fmt.Println(`listening on :3000 (setting the "io" session-affinity cookie)`)

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
