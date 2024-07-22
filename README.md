# go-engine.io

[![Build Workflow](https://github.com/lewisgibson/go-engine.io/actions/workflows/build.yaml/badge.svg)](https://github.com/lewisgibson/go-engine.io/actions/workflows/build.yaml)
[![Pkg Go Dev](https://pkg.go.dev/badge/github.com/lewisgibson/go-engine.io)](https://pkg.go.dev/github.com/lewisgibson/go-engine.io)

An implementation of the [engine.io](https://socket.io/docs/v4/engine-io-protocol/) v4 protocol and client in go.

## Resources

-   [Discussions](https://github.com/lewisgibson/go-engine.io/discussions)
-   [Reference](https://pkg.go.dev/github.com/lewisgibson/go-engine.io)
-   [Examples](https://pkg.go.dev/github.com/lewisgibson/go-engine.io#pkg-examples)

## Features

-   [x] [HTTP Long-Polling Transport](https://socket.io/docs/v4/engine-io-protocol#http-long-polling)
-   [x] [WebSocket Transport](https://socket.io/docs/v4/engine-io-protocol#websocket)
-   [x] [Binary Support](https://socket.io/docs/v4/engine-io-protocol#headers)

## Installation

```sh
go get github.com/lewisgibson/go-engine.io
```

## Quickstart

```go
client, _ := engineio.NewSocket("http://localhost:3000/engine.io/", engineio.SocketOptions{
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

// Bind an open handler.
client.OnOpen(func() {
    client.Send(ctx, []engineio.Packet{
        {Type: engineio.PacketMessage, Data: []byte("Hello")},
    })
})

// Bind a message handler.
client.OnMessage(func(data []byte) {
    fmt.Printf("Message from server: %s\n", string(data))
})

// Bind a close handler.
client.OnClose(func(reason string, cause error) {
    fmt.Printf("Close: %v, %v\n", reason, cause)
})

// Bind an error handler.
client.OnError(func(err error) {
    fmt.Printf("Error: %v\n", err)
})

// Open the client.
client.Open(context.Background())

// Wait for 10 seconds.
<-time.After(time.Second * 10)

// Close the client.
client.Close(context.Background())
```

## Todo

-   [ ] Server implementation
-   [ ] Parse v2/v3 protocol
