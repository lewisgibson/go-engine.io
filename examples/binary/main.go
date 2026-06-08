// Command binary runs a server and a client in one process to demonstrate binary
// messages. The server echoes every message back with its binary flag
// preserved, and the client sends a binary frame, then verifies the echo comes
// back as binary too. Engine.IO carries binary as a message packet; the
// isBinary flag is what distinguishes it from text, so it must be round-tripped
// rather than re-sniffed from the bytes.
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

	// Server: echo each message, keeping isBinary intact so a binary frame comes
	// back as binary and a text frame comes back as text.
	server := engineio.NewServer()
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			fmt.Printf("server received (binary=%t): %x\n", isBinary, data)
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("server send error: %v\n", err)
			}
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

	// Client: send a binary payload once open, and confirm the echo is binary.
	client, err := engineio.NewSocket("http://localhost:3000/engine.io/")
	if err != nil {
		panic(err)
	}

	// done is signalled once the binary echo is observed, so main can exit.
	done := make(chan struct{}, 1)

	client.OnOpen(func() {
		// Send arbitrary bytes as a binary message: setting the Packet's IsBinary
		// field marks it binary, and a binary packet is always a PacketMessage.
		payload := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
		fmt.Printf("client sending (binary): %x\n", payload)
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: payload, IsBinary: true},
		}); err != nil {
			fmt.Printf("client send error: %v\n", err)
		}
	})

	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("client received echo (binary=%t): %x\n", isBinary, data)
		if !isBinary {
			fmt.Println("warning: echo was not binary; the flag was not preserved")
		}
		select {
		case done <- struct{}{}:

		default:
		}
	})

	client.OnError(func(err error) {
		fmt.Printf("client error: %v\n", err)
	})

	client.Open(ctx)
	defer client.Close(ctx)

	// Wait for the binary echo or an interrupt, then shut down.
	select {
	case <-done:

	case <-ctx.Done():
	}

	server.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("shutdown error: %v\n", err)
	}
}
