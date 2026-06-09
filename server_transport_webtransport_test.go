package engineio_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

// clientMessage is a message received by the client, with its binary flag.
type clientMessage struct {
	data     []byte
	isBinary bool
}

// selfSignedCert returns a one-hour self-signed certificate for loopback and a
// pool that trusts it, so a WebTransport client can verify the test server
// without InsecureSkipVerify.
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	// Arrange: a P-256 key and a self-signed cert valid for localhost and the
	// loopback addresses the tests dial.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// newWebTransportDialer returns a dialer that trusts the test certificate. A nil
// QUICConfig lets webtransport-go enable datagrams (required for WebTransport) on
// its own.
func newWebTransportDialer(t *testing.T, pool *x509.CertPool) *webtransport.Dialer {
	t.Helper()

	dialer := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: pool, NextProtos: []string{"h3"}},
	}
	t.Cleanup(func() { dialer.Close() }) //nolint:errcheck // best-effort close in test cleanup

	return dialer
}

// newWebTransportEndpoint builds a *webtransport.Server over mux with cert.
// WithWebTransportServer configures its HTTP/3 server for WebTransport, so the
// caller does not.
func newWebTransportEndpoint(cert tls.Certificate, mux http.Handler) *webtransport.Server {
	return &webtransport.Server{
		H3: &http3.Server{
			TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}},
			Handler:         mux,
			EnableDatagrams: true,
		},
		CheckOrigin: func(*http.Request) bool { return true },
	}
}

// newWebTransportServer starts an Engine.IO server reachable over HTTP/3 on a
// loopback UDP socket, returning its https URL, a pool that trusts its
// certificate, and channels for the first session and its inbound messages.
func newWebTransportServer(t *testing.T) (string, *x509.CertPool, <-chan *engineio.ServerSocket, <-chan serverMessage) {
	t.Helper()

	cert, pool := selfSignedCert(t)

	sockets := make(chan *engineio.ServerSocket, 1)
	messages := make(chan serverMessage, 16)

	mux := http.NewServeMux()
	wt := newWebTransportEndpoint(cert, mux)

	server := engineio.NewServer(
		engineio.WithPingInterval(200*time.Millisecond),
		engineio.WithPingTimeout(2*time.Second),
		engineio.WithServerTransports(engineio.TransportTypeWebTransport),
		engineio.WithWebTransportServer(wt),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			messages <- serverMessage{data: data, isBinary: isBinary}
		})
		select {
		case sockets <- socket:

		default:
		}
	})
	mux.Handle("/engine.io/", server)

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { udpConn.Close() }) //nolint:errcheck // best-effort close in test cleanup

	go func() { wt.Serve(udpConn) }() //nolint:errcheck // serve returns when the listener closes
	t.Cleanup(func() { wt.Close() })  //nolint:errcheck // best-effort close in test cleanup

	url := "https://" + udpConn.LocalAddr().String() + "/engine.io/"

	return url, pool, sockets, messages
}

func TestServer_WebTransport_Initial(t *testing.T) {
	t.Parallel()

	// Arrange: a server reachable over HTTP/3 and a client that starts directly on
	// WebTransport (no polling phase).
	url, pool, sockets, serverMessages := newWebTransportServer(t)

	clientMessages := make(chan clientMessage, 16)
	client, err := engineio.NewSocket(url,
		engineio.WithTransports(engineio.TransportTypeWebTransport),
		engineio.WithWebTransportDialer(newWebTransportDialer(t, pool)),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnMessage(func(data []byte, isBinary bool) { clientMessages <- clientMessage{data: data, isBinary: isBinary} })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	// Act: open and wait for both ends to establish.
	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened
	socket := <-sockets

	// Act + Assert: client -> server text.
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))
	text := <-serverMessages
	require.Equal(t, "hello", string(text.data))
	require.False(t, text.isBinary)

	// Act + Assert: server -> client text.
	require.NoError(t, socket.Send([]byte("world"), false))
	echo := <-clientMessages
	require.Equal(t, "world", string(echo.data))
	require.False(t, echo.isBinary)

	// Act + Assert: client -> server binary.
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
	}))
	binary := <-serverMessages
	require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, binary.data)
	require.True(t, binary.isBinary)

	// Act + Assert: server -> client binary.
	require.NoError(t, socket.Send([]byte{0x05, 0x06, 0x07, 0x08}, true))
	binaryEcho := <-clientMessages
	require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, binaryEcho.data)
	require.True(t, binaryEcho.isBinary)

	// Act + Assert: an empty binary message frames to a zero-length payload, which
	// both go-engine.io peers accept as a valid empty message and round-trip in both
	// directions (a strict reference peer would instead reject the zero-length frame).
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte{}, IsBinary: true},
	}))
	emptyBinary := <-serverMessages
	require.Empty(t, emptyBinary.data)
	require.True(t, emptyBinary.isBinary)

	require.NoError(t, socket.Send([]byte{}, true))
	emptyBinaryEcho := <-clientMessages
	require.Empty(t, emptyBinaryEcho.data)
	require.True(t, emptyBinaryEcho.isBinary)

	// Act + Assert: payloads that exercise the 16-bit and 64-bit framed length
	// paths in both directions round-trip intact.
	medium := strings.Repeat("m", 1_000)
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte(medium)},
	}))
	require.Equal(t, medium, string((<-serverMessages).data))

	large := strings.Repeat("L", 70_000)
	require.NoError(t, socket.Send([]byte(large), false))
	require.Equal(t, large, string((<-clientMessages).data))
}

func TestServer_WebTransport_UpgradeFromPolling(t *testing.T) {
	t.Parallel()

	// Arrange: one Engine.IO server reachable over both TCP (polling) and HTTP/3
	// (webtransport) on the same loopback port, as it would be behind a single
	// origin in production.
	cert, pool := selfSignedCert(t)

	sockets := make(chan *engineio.ServerSocket, 1)
	serverMessages := make(chan serverMessage, 16)

	mux := http.NewServeMux()
	wt := newWebTransportEndpoint(cert, mux)

	server := engineio.NewServer(
		engineio.WithPingInterval(200*time.Millisecond),
		engineio.WithPingTimeout(2*time.Second),
		engineio.WithServerTransports(engineio.TransportTypePolling, engineio.TransportTypeWebTransport),
		engineio.WithWebTransportServer(wt),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			serverMessages <- serverMessage{data: data, isBinary: isBinary}
		})
		select {
		case sockets <- socket:

		default:
		}
	})
	mux.Handle("/engine.io/", server)

	// Bind TCP and UDP to the same loopback port: polling rides the TCP listener
	// and the upgrade probe dials webtransport on the UDP one.
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tcpAddr, ok := tcpListener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	require.NoError(t, err)
	t.Cleanup(func() { udpConn.Close() }) //nolint:errcheck // best-effort close in test cleanup

	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { httpServer.Serve(tcpListener) }() //nolint:errcheck // serve returns when the listener closes
	t.Cleanup(func() { httpServer.Close() })      //nolint:errcheck // best-effort close in test cleanup

	go func() { wt.Serve(udpConn) }() //nolint:errcheck // serve returns when the listener closes
	t.Cleanup(func() { wt.Close() })  //nolint:errcheck // best-effort close in test cleanup

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/engine.io/"

	clientMessages := make(chan clientMessage, 16)
	upgraded := make(chan engineio.TransportType, 1)
	client, err := engineio.NewSocket(url,
		engineio.WithUpgrade(true),
		engineio.WithTransports(engineio.TransportTypePolling, engineio.TransportTypeWebTransport),
		engineio.WithWebTransportDialer(newWebTransportDialer(t, pool)),
	)
	require.NoError(t, err)

	opened := make(chan struct{}, 1)
	client.OnOpen(func() { opened <- struct{}{} })
	client.OnUpgrade(func(transportType engineio.TransportType) {
		select {
		case upgraded <- transportType:

		default:
		}
	})
	client.OnMessage(func(data []byte, isBinary bool) { clientMessages <- clientMessage{data: data, isBinary: isBinary} })
	client.OnError(func(error) {})
	client.OnPacket(func(engineio.Packet) {})

	// Act: open, wait for the handshake, then wait for the upgrade to webtransport.
	// Keying on the upgrade event makes the round trips below deterministic.
	client.Open(t.Context())
	t.Cleanup(func() { client.Close(t.Context()) })
	<-opened
	socket := <-sockets
	require.Equal(t, engineio.TransportTypeWebTransport, <-upgraded)

	// Act + Assert: client -> server over the upgraded webtransport. Receiving it
	// proves the server processed the upgrade packet that preceded it.
	require.NoError(t, client.Send(t.Context(), []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("hello")},
	}))
	text := <-serverMessages
	require.Equal(t, "hello", string(text.data))
	require.False(t, text.isBinary)

	// Act + Assert: server -> client binary over the upgraded webtransport.
	require.NoError(t, socket.Send([]byte{0x05, 0x06, 0x07, 0x08}, true))
	echo := <-clientMessages
	require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, echo.data)
	require.True(t, echo.isBinary)
}

func TestServer_WebTransport_RoutesConnectWithoutQuery(t *testing.T) {
	t.Parallel()

	// Arrange: a browser's WebTransport CONNECT carries no EIO query (the session is
	// identified by its first stream packet), so the server must route it by its
	// CONNECT signature. With WebTransport not wired, that routing reports an unknown
	// transport -- whereas the query-based path would wrongly report an unsupported
	// protocol version on the missing EIO query.
	errs := make(chan engineio.ConnectionErrorCode, 1)
	server := engineio.NewServer()
	server.OnConnectionError(func(_ *http.Request, code engineio.ConnectionErrorCode, _ string) {
		errs <- code
	})

	request := httptest.NewRequest(http.MethodConnect, "/engine.io/", nil)
	request.Proto = "webtransport"

	// Act: dispatch the query-less WebTransport CONNECT.
	server.ServeHTTP(httptest.NewRecorder(), request)

	// Assert: it was routed by the CONNECT signature, not rejected on the missing
	// EIO query.
	require.Equal(t, engineio.ConnectionErrorUnknownTransport, <-errs)
}
