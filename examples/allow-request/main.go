// Command allow-request shows how to gate Engine.IO handshakes with
// WithAllowRequest: the gate runs before a session is allocated, so it is the
// place for authentication, token checks, and rate limiting. A rejected
// handshake is answered with HTTP 403, and OnConnectionError surfaces the
// rejection for logging.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	server := engineio.NewServer(
		// Gate every handshake. Returning a non-nil error rejects it with 403.
		engineio.WithAllowRequest(func(r *http.Request) error {
			// Require a non-empty "Bearer <token>". A real implementation would
			// validate a JWT or look the token up in a session store; here any
			// non-empty token is accepted.
			if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok || token == "" {
				return errors.New("missing or invalid token")
			}

			return nil
		}),
	)

	// Observe rejected handshakes for logging and alerting.
	server.OnConnectionError(func(r *http.Request, code engineio.ConnectionErrorCode, reason string) {
		fmt.Printf("rejected %s (code %d): %s\n", r.RemoteAddr, code, reason)
	})

	server.OnConnection(func(socket *engineio.ServerSocket) {
		fmt.Printf("authorized session: %s\n", socket.ID())

		socket.OnMessage(func(data []byte, isBinary bool) {
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("send error: %v\n", err)
			}
		})
	})

	mux := http.NewServeMux()
	mux.Handle("/engine.io/", server)

	var httpServer = &http.Server{
		Addr:              ":3000",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		fmt.Printf("server error: %v\n", err)
	}
}
