// Command webtransport runs a server and a client in one process to demonstrate
// the WebTransport (HTTP/3) transport. WebTransport always runs over QUIC (UDP)
// with TLS, so the example generates a throwaway self-signed certificate, serves
// the Engine.IO handler on an HTTP/3 listener, and connects a client that trusts
// that certificate and starts directly on WebTransport.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// WebTransport requires TLS; generate a throwaway certificate for loopback and
	// a pool the client uses to trust it. A real deployment uses a CA-issued cert.
	cert, pool := selfSignedCert()

	// The WebTransport server upgrades HTTP/3 CONNECT requests into sessions. It is
	// built before the Engine.IO server so the server can be wired to it (which also
	// configures it for WebTransport), and it serves the same mux.
	mux := http.NewServeMux()
	wt := &webtransport.Server{
		H3: &http3.Server{
			TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}},
			Handler:         mux,
			EnableDatagrams: true,
		},
		CheckOrigin: func(*http.Request) bool { return true },
	}

	// Server: echo every message back to its sender, accepting WebTransport.
	server := engineio.NewServer(
		engineio.WithServerTransports(engineio.TransportTypeWebTransport),
		engineio.WithWebTransportServer(wt),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(data []byte, isBinary bool) {
			fmt.Printf("server received: %q\n", data)
			if err := socket.Send(data, isBinary); err != nil {
				fmt.Printf("server send error: %v\n", err)
			}
		})
	})
	mux.Handle("/engine.io/", server)

	// Listen on a loopback UDP socket; the OS picks a free port so the example
	// never collides with another process.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		panic(err)
	}
	go func() {
		if err := wt.Serve(udpConn); err != nil {
			fmt.Printf("server error: %v\n", err)
		}
	}()
	defer wt.Close() //nolint:errcheck // best-effort close on shutdown

	url := "https://" + udpConn.LocalAddr().String() + "/engine.io/"
	fmt.Printf("listening on %s\n", url)

	// Client: dial WebTransport with a dialer that trusts the test certificate.
	client, err := engineio.NewSocket(url,
		engineio.WithTransports(engineio.TransportTypeWebTransport),
		engineio.WithWebTransportDialer(&webtransport.Dialer{
			TLSClientConfig: &tls.Config{RootCAs: pool, NextProtos: []string{"h3"}},
		}),
	)
	if err != nil {
		panic(err)
	}

	// done is signalled once the echo is observed, so main can exit.
	done := make(chan struct{}, 1)

	client.OnOpen(func() {
		fmt.Println("client connected over webtransport")
		if err := client.Send(ctx, []engineio.Packet{
			{Type: engineio.PacketMessage, Data: []byte("hello over webtransport")},
		}); err != nil {
			fmt.Printf("client send error: %v\n", err)
		}
	})
	client.OnMessage(func(data []byte, _ bool) {
		fmt.Printf("client received echo: %q\n", data)
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

	// Wait for the echo or an interrupt, then shut down.
	select {
	case <-done:

	case <-ctx.Done():
	}

	server.Close()
}

// selfSignedCert generates a throwaway certificate for loopback and a pool that
// trusts it. It panics on failure, since the example cannot proceed without it.
func selfSignedCert() (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
