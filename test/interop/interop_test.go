//go:build interop
// +build interop

package interop

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// interopMessage is a message observed by one side of an interop round trip,
// along with whether it arrived as a binary frame.
type interopMessage struct {
	data     []byte
	isBinary bool
}

// interopPayloads is the set of payloads every transport must round-trip
// unchanged in both directions: plain text, an empty message, multi-byte UTF-8,
// raw binary including NUL and high bytes, and large text and binary bodies.
var interopPayloads = []struct {
	name     string
	data     []byte
	isBinary bool
}{
	{name: "text", data: []byte("hello"), isBinary: false},
	{name: "empty_text", data: []byte(""), isBinary: false},
	{name: "multibyte_text", data: []byte("h\u00e9llo \u4e16\u754c \u20ac"), isBinary: false},
	{name: "binary", data: []byte{0x00, 0x01, 0x02, 0xfe, 0xff}, isBinary: true},
	{name: "large_text", data: []byte(strings.Repeat("x", 32*1024)), isBinary: false},
	{name: "large_binary", data: bytes.Repeat([]byte{0xab, 0xcd}, 16*1024), isBinary: true},
}

// requireNodeModules skips the test when the npm dependencies are not installed.
func requireNodeModules(t *testing.T) {
	t.Helper()

	if _, err := os.Stat("node_modules/engine.io"); err != nil {
		t.Skip("run `npm --prefix test/interop ci` to install the JS dependencies")
	}
}

// recv waits for a value on a channel or fails the test.
func recv[T any](t *testing.T, channel <-chan T, message string) T {
	t.Helper()

	select {
	case value := <-channel:
		return value

	case <-time.After(10 * time.Second):
		require.FailNow(t, message)
		var zero T
		return zero
	}
}

// stopProcess shuts a child process down gracefully: closing stdin asks the JS
// programs to exit, and the process is killed only if it does not exit promptly.
// The Wait error is consumed via the channel; a kill error is logged rather than
// discarded.
func stopProcess(t *testing.T, cmd *exec.Cmd, stdin io.WriteCloser) {
	t.Helper()

	if err := stdin.Close(); err != nil {
		t.Logf("closing child stdin: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:

	case <-time.After(2 * time.Second):
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("killing child process: %v", err)
		}
		<-done
	}
}

// startJSProcess launches a node script with a stdin pipe (for clean shutdown)
// and a scanned stdout, returning a channel of stdout lines.
func startJSProcess(t *testing.T, script string, args ...string) <-chan string {
	t.Helper()

	requireNodeModules(t)

	cmd := exec.Command("node", append([]string{script}, args...)...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { stopProcess(t, cmd, stdin) })

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	return lines
}

// startJSServer launches the reference JS engine.io echo server and returns the
// port it is listening on.
func startJSServer(t *testing.T) int {
	t.Helper()

	lines := startJSProcess(t, "js/server.mjs")

	for {
		line := recv(t, lines, "JS server did not report a port")
		if rest, ok := strings.CutPrefix(line, "LISTENING "); ok {
			port, err := strconv.Atoi(rest)
			require.NoError(t, err)

			return port
		}
	}
}

// jsClient is a handle to a running reference JS engine.io echo client.
type jsClient struct {
	ready    <-chan struct{}
	upgraded <-chan struct{}
}

// startJSClient launches the reference JS echo client against url with the given
// transports ("polling", "websocket", or "polling,websocket"). It returns a
// handle whose channels fire once the client opens and once it upgrades.
func startJSClient(t *testing.T, url, transports string) *jsClient {
	t.Helper()

	lines := startJSProcess(t, "js/client.mjs", url, transports)

	ready := make(chan struct{}, 1)
	upgraded := make(chan struct{}, 1)
	go func() {
		for line := range lines {
			switch line {
			case "READY":
				select {
				case ready <- struct{}{}:

				default:
				}

			case "UPGRADED":
				select {
				case upgraded <- struct{}{}:

				default:
				}
			}
		}
	}()

	return &jsClient{ready: ready, upgraded: upgraded}
}

// newGoClient opens a Go client against the JS server on port with the given
// transports and returns it alongside a channel of the messages it receives.
// When more than one transport is given it waits for the upgrade to complete, so
// later round trips run over a settled transport.
func newGoClient(t *testing.T, port int, transports ...engineio.TransportType) (*engineio.Socket, <-chan interopMessage) {
	t.Helper()

	messages := make(chan interopMessage, 64)
	upgrade := len(transports) > 1

	client, err := engineio.NewSocket(
		fmt.Sprintf("http://127.0.0.1:%d/engine.io/", port),
		engineio.WithTransports(transports...),
		engineio.WithUpgrade(upgrade),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	upgraded := make(chan struct{}, 1)
	client.OnOpen(func() {
		select {
		case opened <- struct{}{}:

		default:
		}
	})
	client.OnUpgrade(func(engineio.TransportType) {
		select {
		case upgraded <- struct{}{}:

		default:
		}
	})
	client.OnMessage(func(data []byte, isBinary bool) {
		messages <- interopMessage{data: append([]byte(nil), data...), isBinary: isBinary}
	})
	client.OnError(func(error) {})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(context.WithoutCancel(t.Context())) })

	recv(t, opened, "go client did not open")
	if upgrade {
		recv(t, upgraded, "go client did not upgrade")
	}

	return client, messages
}

// newGoServer starts a real Go Engine.IO server and returns its Engine.IO URL, a
// channel of accepted sessions, and a channel of the messages those sessions
// receive. The heartbeat is brisk but well within timeout so sessions stay alive.
func newGoServer(t *testing.T) (string, <-chan *engineio.ServerSocket, <-chan interopMessage) {
	t.Helper()

	sockets := make(chan *engineio.ServerSocket, 8)
	received := make(chan interopMessage, 64)

	server := engineio.NewServer(
		engineio.WithPingInterval(300*time.Millisecond),
		engineio.WithPingTimeout(5*time.Second),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			received <- interopMessage{data: append([]byte(nil), data...), isBinary: isBinary}
		})
		sockets <- socket
	})

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	return httpServer.URL + "/engine.io/", sockets, received
}

// requireEqualMessage asserts an echoed message matches the payload that was
// sent, treating a nil and an empty body as equal.
func requireEqualMessage(t *testing.T, name string, want, got interopMessage) {
	t.Helper()

	require.Truef(t, bytes.Equal(want.data, got.data), "data mismatch for %s", name)
	require.Equalf(t, want.isBinary, got.isBinary, "binary flag mismatch for %s", name)
}

// requireClientEcho sends a payload from the Go client and asserts the JS server
// echoes it back unchanged.
func requireClientEcho(t *testing.T, client *engineio.Socket, messages <-chan interopMessage, name string, payload interopMessage) {
	t.Helper()

	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: payload.data, IsBinary: payload.isBinary},
	}))
	requireEqualMessage(t, name, payload, recv(t, messages, "no echo from JS server: "+name))
}

// requireServerEcho sends a payload from the Go server and asserts the JS client
// echoes it back unchanged.
func requireServerEcho(t *testing.T, socket *engineio.ServerSocket, received <-chan interopMessage, name string, payload interopMessage) {
	t.Helper()

	require.NoError(t, socket.Send(payload.data, payload.isBinary))
	requireEqualMessage(t, name, payload, recv(t, received, "no echo from JS client: "+name))
}

// --- Go client against the reference JS server -----------------------------

func TestInterop_GoClientToJSServer_Polling(t *testing.T) {
	t.Parallel()

	port := startJSServer(t)
	client, messages := newGoClient(t, port, engineio.TransportTypePolling)

	for _, payload := range interopPayloads {
		requireClientEcho(t, client, messages, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_GoClientToJSServer_WebSocket(t *testing.T) {
	t.Parallel()

	port := startJSServer(t)
	client, messages := newGoClient(t, port, engineio.TransportTypeWebSocket)

	for _, payload := range interopPayloads {
		requireClientEcho(t, client, messages, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_GoClientToJSServer_Upgrade(t *testing.T) {
	t.Parallel()

	port := startJSServer(t)
	client, messages := newGoClient(t, port, engineio.TransportTypePolling, engineio.TransportTypeWebSocket)

	for _, payload := range interopPayloads {
		requireClientEcho(t, client, messages, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_GoClientToJSServer_BurstOrdering(t *testing.T) {
	t.Parallel()

	const count = 200

	port := startJSServer(t)
	client, messages := newGoClient(t, port, engineio.TransportTypePolling, engineio.TransportTypeWebSocket)

	// Act: fire a burst back to back without waiting for echoes
	for index := range count {
		require.NoError(t, client.Send(t.Context(), []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte(strconv.Itoa(index))},
		}))
	}

	// Assert: every message echoes back exactly once and in order
	for index := range count {
		got := recv(t, messages, "missing burst echo")
		require.Equal(t, strconv.Itoa(index), string(got.data))
	}
}

func TestInterop_GoClientToJSServer_PollingBurstOrdering(t *testing.T) {
	t.Parallel()

	const count = 200

	port := startJSServer(t)
	client, messages := newGoClient(t, port, engineio.TransportTypePolling)

	// Act: fire a burst over long-polling so several packets batch into each POST
	// body, exercising the v4 record-separated payload framing the JS server must
	// de-frame (the upgraded-transport burst test runs over websocket instead).
	for index := range count {
		require.NoError(t, client.Send(t.Context(), []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte(strconv.Itoa(index))},
		}))
	}

	// Assert: every message echoes back exactly once and in order
	for index := range count {
		got := recv(t, messages, "missing burst echo")
		require.Equal(t, strconv.Itoa(index), string(got.data))
	}
}

func TestInterop_GoClientToJSServer_SendDuringUpgrade(t *testing.T) {
	t.Parallel()

	const count = 200

	port := startJSServer(t)

	// Arrange: a client that upgrades, but fire the burst without waiting for the
	// upgrade to settle, so writes span the background polling->websocket switch.
	messages := make(chan interopMessage, count)
	opened := make(chan struct{}, 1)

	client, err := engineio.NewSocket(
		fmt.Sprintf("http://127.0.0.1:%d/engine.io/", port),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebSocket),
		engineio.WithUpgrade(true),
	)
	require.NoError(t, err)
	client.OnOpen(func() {
		select {
		case opened <- struct{}{}:

		default:
		}
	})
	client.OnMessage(func(data []byte, isBinary bool) {
		messages <- interopMessage{data: append([]byte(nil), data...), isBinary: isBinary}
	})
	client.OnError(func(error) {})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(context.WithoutCancel(t.Context())) })
	recv(t, opened, "go client did not open")

	// Act: burst immediately, while the upgrade probe runs in the background, so
	// packets leave over polling and the rest over the websocket once the switch
	// commits -- writes must be buffered across the switch, never lost or reordered.
	for index := range count {
		require.NoError(t, client.Send(t.Context(), []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte(strconv.Itoa(index))},
		}))
	}

	// Assert: every message echoes back exactly once and in order, wherever in the
	// burst the upgrade landed.
	for index := range count {
		got := recv(t, messages, "missing echo during upgrade")
		require.Equal(t, strconv.Itoa(index), string(got.data))
	}
}

func TestInterop_GoClientToJSServer_Heartbeat(t *testing.T) {
	t.Parallel()

	port := startJSServer(t)

	pings := make(chan struct{}, 16)
	messages := make(chan interopMessage, 4)
	opened := make(chan struct{}, 1)

	client, err := engineio.NewSocket(
		fmt.Sprintf("http://127.0.0.1:%d/engine.io/", port),
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)
	client.OnOpen(func() {
		select {
		case opened <- struct{}{}:

		default:
		}
	})
	client.OnPacket(func(packet engineio.Packet) {
		if packet.Type == engineio.PacketPing {
			select {
			case pings <- struct{}{}:

			default:
			}
		}
	})
	client.OnMessage(func(data []byte, isBinary bool) {
		messages <- interopMessage{data: append([]byte(nil), data...), isBinary: isBinary}
	})
	client.OnError(func(error) {})

	client.Open(t.Context())
	t.Cleanup(func() { client.Close(context.WithoutCancel(t.Context())) })
	recv(t, opened, "go client did not open")

	// Assert: the client answers several server pings (the JS server pings every
	// 100ms and would drop a client that stopped ponging)...
	for range 3 {
		recv(t, pings, "missing server ping")
	}

	// ...and the session is still healthy afterwards.
	requireClientEcho(t, client, messages, "post-heartbeat", interopMessage{data: []byte("still here")})
}

func TestInterop_GoClientToJSServer_ClientClose(t *testing.T) {
	t.Parallel()

	port := startJSServer(t)

	closed := make(chan string, 1)
	opened := make(chan struct{}, 1)

	client, err := engineio.NewSocket(
		fmt.Sprintf("http://127.0.0.1:%d/engine.io/", port),
		engineio.WithTransports(engineio.TransportTypePolling),
		engineio.WithUpgrade(false),
	)
	require.NoError(t, err)
	client.OnOpen(func() {
		select {
		case opened <- struct{}{}:

		default:
		}
	})
	client.OnClose(func(reason string, _ error) { closed <- reason })
	client.OnError(func(error) {})

	client.Open(t.Context())
	recv(t, opened, "go client did not open")

	// Act: close the client
	client.Close(t.Context())

	// Assert: the close handler fires
	recv(t, closed, "go client close handler did not fire")
}

// --- Reference JS client against the Go server -----------------------------

func TestInterop_JSClientToGoServer_Polling(t *testing.T) {
	t.Parallel()

	url, sockets, received := newGoServer(t)
	startJSClient(t, url, "polling")
	socket := recv(t, sockets, "go server did not accept the JS client")

	for _, payload := range interopPayloads {
		requireServerEcho(t, socket, received, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_JSClientToGoServer_WebSocket(t *testing.T) {
	t.Parallel()

	url, sockets, received := newGoServer(t)
	startJSClient(t, url, "websocket")
	socket := recv(t, sockets, "go server did not accept the JS client")

	for _, payload := range interopPayloads {
		requireServerEcho(t, socket, received, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_JSClientToGoServer_Upgrade(t *testing.T) {
	t.Parallel()

	url, sockets, received := newGoServer(t)
	client := startJSClient(t, url, "polling,websocket")
	socket := recv(t, sockets, "go server did not accept the JS client")

	// Wait for the upgrade so the round trips run over the websocket transport.
	recv(t, client.upgraded, "JS client did not upgrade")

	for _, payload := range interopPayloads {
		requireServerEcho(t, socket, received, payload.name, interopMessage{data: payload.data, isBinary: payload.isBinary})
	}
}

func TestInterop_JSClientToGoServer_BurstOrdering(t *testing.T) {
	t.Parallel()

	const count = 200

	url, sockets, received := newGoServer(t)
	client := startJSClient(t, url, "polling,websocket")
	socket := recv(t, sockets, "go server did not accept the JS client")
	recv(t, client.upgraded, "JS client did not upgrade")

	// Act: fire a burst from the server back to back
	for index := range count {
		require.NoError(t, socket.Send([]byte(strconv.Itoa(index)), false))
	}

	// Assert: every message echoes back exactly once and in order
	for index := range count {
		got := recv(t, received, "missing burst echo")
		require.Equal(t, strconv.Itoa(index), string(got.data))
	}
}

func TestInterop_JSClientToGoServer_PollingBurstOrdering(t *testing.T) {
	t.Parallel()

	const count = 200

	url, sockets, received := newGoServer(t)
	startJSClient(t, url, "polling")
	socket := recv(t, sockets, "go server did not accept the JS client")

	// Act: fire a burst from the Go server over long-polling so several packets
	// batch into each poll response body that the JS client must de-frame.
	for index := range count {
		require.NoError(t, socket.Send([]byte(strconv.Itoa(index)), false))
	}

	// Assert: every message echoes back exactly once and in order
	for index := range count {
		got := recv(t, received, "missing burst echo")
		require.Equal(t, strconv.Itoa(index), string(got.data))
	}
}

func TestInterop_JSClientToGoServer_Heartbeat(t *testing.T) {
	t.Parallel()

	// Arrange: a Go server with a brisk ping and a short pong timeout, so a JS
	// client whose pongs were not landing would be dropped within a few hundred ms.
	sockets := make(chan *engineio.ServerSocket, 1)
	received := make(chan interopMessage, 4)
	closed := make(chan struct{}, 1)

	server := engineio.NewServer(
		engineio.WithPingInterval(150*time.Millisecond),
		engineio.WithPingTimeout(300*time.Millisecond),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			received <- interopMessage{data: append([]byte(nil), data...), isBinary: isBinary}
		})
		socket.OnClose(func(string, error) {
			select {
			case closed <- struct{}{}:

			default:
			}
		})
		sockets <- socket
	})
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	startJSClient(t, httpServer.URL+"/engine.io/", "websocket")
	socket := recv(t, sockets, "go server did not accept the JS client")

	// Assert: the session survives well past several ping/pong cycles -- if the JS
	// client's pongs were not reaching the Go server, it would have closed the
	// session on the 300ms timeout long before this.
	select {
	case <-closed:
		require.FailNow(t, "go server dropped the JS client despite its pongs")

	case <-time.After(2 * time.Second):
	}

	// ...and the session is still healthy.
	requireServerEcho(t, socket, received, "post-heartbeat", interopMessage{data: []byte("still here")})
}

func TestInterop_JSClientToGoServer_MultipleClients(t *testing.T) {
	t.Parallel()

	const clients = 3

	url, sockets, received := newGoServer(t)

	// Arrange: connect several independent JS clients
	for range clients {
		startJSClient(t, url, "polling,websocket")
	}

	var accepted []*engineio.ServerSocket
	for range clients {
		accepted = append(accepted, recv(t, sockets, "go server did not accept all JS clients"))
	}

	// Act: send each session a unique message
	want := make(map[string]bool, clients)
	for index, socket := range accepted {
		message := fmt.Sprintf("client-%d", index)
		want[message] = true
		require.NoError(t, socket.Send([]byte(message), false))
	}

	// Assert: every session echoed its own message back
	for range clients {
		got := recv(t, received, "missing echo from a JS client")
		require.True(t, want[string(got.data)], "unexpected echo %q", string(got.data))
		delete(want, string(got.data))
	}
	require.Empty(t, want)
}

func TestInterop_JSClientToGoServer_ServerClose(t *testing.T) {
	t.Parallel()

	url, sockets, _ := newGoServer(t)

	closed := make(chan string, 1)
	client := startJSClient(t, url, "polling")
	socket := recv(t, sockets, "go server did not accept the JS client")
	socket.OnClose(func(reason string, _ error) { closed <- reason })

	// Wait for the client to be fully open before closing it.
	recv(t, client.ready, "JS client did not report ready")

	// Act: close the session from the server side
	require.NoError(t, socket.Close())

	// Assert: the server close handler fires
	recv(t, closed, "go server close handler did not fire")
}
