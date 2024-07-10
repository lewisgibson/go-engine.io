package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	closed := make(chan struct{}, 1)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)

	client, err := engineio.NewSocket("http://localhost:3000/engine.io/", engineio.SocketOptions{
		Client: &http.Client{
			Timeout: time.Second * 30,
		},
		Header: &http.Header{
			"Authorization": []string{"Bearer token"},
		},
		Upgrade:         engineio.Pointer(true),
		RememberUpgrade: engineio.Pointer(true),
		Transports: &[]engineio.TransportType{
			engineio.TransportTypePolling,
			engineio.TransportTypeWebSocket,
		},
	})
	if err != nil {
		panic(err)
	}

	client.OnMessage(func(data []byte) {
		fmt.Printf("Message from server: %s\n", string(data))
		client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: data},
		})
	})

	client.OnOpen(func() {
		client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("Hello")},
		})
	})

	client.OnClose(func(reason string, cause error) {
		fmt.Printf("Close: %v, %v\n", reason, cause)
		closed <- struct{}{}
	})

	client.OnError(func(err error) {
		fmt.Printf("Error: %v\n", err)
	})

	client.Open(ctx)
	defer client.Close(ctx)

	select {
	case <-closed:
	case <-sigs:
	}
}
