// Command net-http mounts an Engine.IO server on a standard net/http ServeMux at a custom
// path, alongside ordinary HTTP routes. The server is an http.Handler, so it
// composes with the rest of an application's routing the same way any other
// handler does.
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

	// Mount the Engine.IO server at a non-default path. The trailing slash makes
	// this a subtree match, so every transport sub-path (handshake, polling, and
	// the websocket upgrade) under it is routed to the server. Clients must use
	// this same path in their server URL.
	mux.Handle("/realtime/engine.io/", server)

	// Ordinary routes live alongside the realtime endpoint on the same mux.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("homepage\n")); err != nil {
			fmt.Printf("write error: %v\n", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok\n")); err != nil {
			fmt.Printf("write error: %v\n", err)
		}
	})

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
	fmt.Println("listening on :3000 (engine.io at /realtime/engine.io/, also try / and /healthz)")

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
