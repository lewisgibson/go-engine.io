// Command graceful-shutdown runs a server that shuts down cleanly on SIGINT or SIGTERM. On the
// signal it closes every live session via Server.Close (each session's OnClose
// fires with reason "forced close"), then drains the HTTP server. Server.Close
// only tears down the Engine.IO sessions; the underlying http.Server is shut
// down separately, so both steps are needed for a clean exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	// signal.NotifyContext cancels ctx on the first SIGINT or SIGTERM, which is
	// how a container orchestrator (SIGTERM) and an interactive Ctrl+C (SIGINT)
	// both ask the process to stop.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("connected: %s\n", socket.ID())

		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})

		// On shutdown each session closes with reason "forced close"; logging it
		// makes the graceful teardown visible.
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
	fmt.Println("listening on :3000 (send SIGINT or SIGTERM to shut down)")

	// Block until a shutdown signal arrives.
	<-ctx.Done()
	fmt.Println("shutting down: closing all sessions")

	// Close every Engine.IO session first, firing each socket's close handler.
	server.Close()

	// Then drain the HTTP server, bounding how long it waits for in-flight
	// requests (the long-poll holds among them) to finish.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
	fmt.Println("shutdown complete")
}
