// Command auth-session-id runs an Engine.IO server whose session identifiers embed the authenticated
// user resolved from the handshake request. It is the headline example for
// WithGenerateID, which receives the *http.Request so the identifier can be
// derived from request data such as a bearer token.
//
// A session id is a bearer credential: the server routes every poll and POST on
// the id alone, so anyone who holds it can read the session's messages and inject
// into it. The id must therefore be unguessable. This example prefixes it with the
// resolved user for log and trace attribution, then appends crypto-random bytes so
// the full id still cannot be guessed or enumerated. The prefix is a convenience
// for humans reading logs; the random suffix is what makes the id safe.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// users stands in for an identity store, mapping a bearer token to a display
	// name. A real server would verify a JWT or look the token up in a session
	// store or identity provider.
	users := map[string]string{
		"token-alice": "alice",
		"token-bob":   "bob",
	}

	server := engineio.NewServer(
		engineio.WithGenerateID(func(r *http.Request) string {
			// Resolve the caller from the Authorization header for attribution. An
			// anonymous or unknown caller still gets a session here; gate access by
			// rejecting the request with WithAllowRequest (see the allow-request
			// example) rather than by returning a sentinel id.
			var user = users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			if user == "" {
				user = "anon"
			}

			// Append crypto-random entropy so the id is unguessable and unique per
			// session, since one user may hold several live sessions (tabs, devices).
			var random [18]byte
			if _, err := rand.Read(random[:]); err != nil {
				// crypto/rand.Read does not fail on supported platforms; never hand
				// out a predictable identifier.
				panic("engineio example: read random: " + err.Error())
			}

			return user + "-" + base64.RawURLEncoding.EncodeToString(random[:])
		}),
	)

	server.OnConnection(func(socket *engineio.ServerSocket) {
		// The id is prefixed with the resolved user, so it is meaningful in logs.
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
	fmt.Println("listening on :3000 (try: Authorization: Bearer token-alice)")

	// Wait for an interrupt, then shut down gracefully.
	<-ctx.Done()
	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
