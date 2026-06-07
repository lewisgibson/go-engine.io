package engineio_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// benchSmallText is a small text message packet.
var benchSmallText = engineio.Packet{Type: engineio.PacketMessage, Data: []byte("hello world")}

// benchLargeText is a roughly 1KB text message packet.
var benchLargeText = engineio.Packet{Type: engineio.PacketMessage, Data: []byte(strings.Repeat("x", 1024))}

// benchBinary is a binary message packet that is base64-encoded on the wire.
var benchBinary = engineio.Packet{Type: engineio.PacketMessage, Data: bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64), IsBinary: true}

// benchTextSizes is a sweep of text message payload sizes (in bytes) used by the
// size-sensitive codec benchmarks, from a tiny packet through roughly 1KB to a
// large 64KB packet, so the allocation and copy costs show how they scale.
var benchTextSizes = []int{16, 1024, 65536}

// benchTextPacket builds a text message packet of size bytes filled with 'x'.
func benchTextPacket(size int) engineio.Packet {
	return engineio.Packet{Type: engineio.PacketMessage, Data: []byte(strings.Repeat("x", size))}
}

// benchBinaryPacket builds a binary message packet of size bytes of incrementing
// values, so the base64 framing has realistic, non-trivial content.
func benchBinaryPacket(size int) engineio.Packet {
	var data = make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}

	return engineio.Packet{Type: engineio.PacketMessage, Data: data, IsBinary: true}
}

// BenchmarkEncodePacket measures encoding a single packet to its text wire form.
func BenchmarkEncodePacket(b *testing.B) {
	packets := map[string]engineio.Packet{
		"small_text": benchSmallText,
		"large_text": benchLargeText,
		"binary":     benchBinary,
	}

	for name, packet := range packets {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = engineio.EncodePacket(packet)
			}
		})
	}
}

// BenchmarkDecodePacket measures decoding a single packet from its text wire form.
func BenchmarkDecodePacket(b *testing.B) {
	inputs := map[string][]byte{
		"small_text": engineio.EncodePacket(benchSmallText),
		"large_text": engineio.EncodePacket(benchLargeText),
		"binary":     engineio.EncodePacket(benchBinary),
	}

	for name, input := range inputs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, err := engineio.DecodePacket(input)
				require.NoError(b, err)
			}
		})
	}
}

// BenchmarkEncodePacket_Sizes measures encoding a single text packet across a
// sweep of payload sizes, so the per-byte cost of the wire encoding (the type
// byte prefix plus the data copy) is visible as the payload grows.
func BenchmarkEncodePacket_Sizes(b *testing.B) {
	for _, size := range benchTextSizes {
		packet := benchTextPacket(size)
		b.Run(strconv.Itoa(size)+"B", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = engineio.EncodePacket(packet)
			}
		})
	}
}

// BenchmarkEncodeBinaryPacket_Sizes measures encoding a single binary packet
// across a sweep of payload sizes, isolating the base64 framing cost (which
// grows by 4/3 over the raw byte count) from the plain text encoding path.
func BenchmarkEncodeBinaryPacket_Sizes(b *testing.B) {
	for _, size := range benchTextSizes {
		packet := benchBinaryPacket(size)
		b.Run(strconv.Itoa(size)+"B", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = engineio.EncodePacket(packet)
			}
		})
	}
}

// BenchmarkPacketRoundTrip measures a full single-packet round trip
// (EncodePacket then DecodePacket) for text and binary packets. The binary case
// exercises the base64 frame on both the encode and the decode side, which is
// the costliest single-packet path because both directions allocate.
func BenchmarkPacketRoundTrip(b *testing.B) {
	packets := map[string]engineio.Packet{
		"small_text": benchSmallText,
		"large_text": benchLargeText,
		"binary":     benchBinary,
	}

	for name, packet := range packets {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				decoded, err := engineio.DecodePacket(engineio.EncodePacket(packet))
				require.NoError(b, err)
				_ = decoded
			}
		})
	}
}

// BenchmarkEncodePayload measures encoding a v4 long-polling payload.
func BenchmarkEncodePayload(b *testing.B) {
	small := []engineio.Packet{benchSmallText, {Type: engineio.PacketPing}, benchSmallText}

	var large = make([]engineio.Packet, 50)
	for i := range large {
		large[i] = benchSmallText
	}

	payloads := map[string][]engineio.Packet{
		"small": small,
		"large": large,
	}

	for name, packets := range payloads {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = engineio.EncodePayload(packets)
			}
		})
	}
}

// BenchmarkDecodePayload measures decoding a long-polling payload for each
// negotiated protocol version and framing.
//
// The euro sign U+20AC is one rune but three UTF-8 bytes; the v2/v3 string
// framing counts runes, so it is included to exercise the multibyte-rune path.
// It is written as \u20ac to keep the source ASCII-only.
func BenchmarkDecodePayload(b *testing.B) {
	tests := []struct {
		name    string
		version engineio.ProtocolVersion
		input   []byte
	}{
		{
			name:    "v4_record_separator",
			version: engineio.ProtocolVersion4,
			input:   []byte("4hello\x1e2\x1e4world\x1ebAQIDBA=="),
		},
		{
			name:    "v3_string_framing",
			version: engineio.ProtocolVersion3,
			input:   []byte("6:4hello2:4\u20ac10:b4AQIDBA=="),
		},
		{
			name:    "v3_binary_framing",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x00, 0x04, 0xff, 0x34, 0xe2, 0x82, 0xac, 0x01, 0x05, 0xff, 0x04, 0x01, 0x02, 0x03, 0x04},
		},
		{
			name:    "v2_string_framing",
			version: engineio.ProtocolVersion2,
			input:   []byte("6:4hello6:4world"),
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, err := engineio.DecodePayload(tt.version, tt.input)
				require.NoError(b, err)
			}
		})
	}
}

// BenchmarkPayloadRoundTrip measures a full v4 payload round trip
// (EncodePayload then DecodePayload) for a realistic batch of mixed packets, the
// shape a long-polling exchange carries: a couple of messages and a heartbeat.
// It is the combined cost of the framing in both directions for one poll cycle.
func BenchmarkPayloadRoundTrip(b *testing.B) {
	batch := []engineio.Packet{benchSmallText, {Type: engineio.PacketPing}, benchSmallText, benchBinary}

	b.ReportAllocs()
	for b.Loop() {
		encoded := engineio.EncodePayload(batch)
		decoded, err := engineio.DecodePayload(engineio.ProtocolVersion4, encoded)
		require.NoError(b, err)
		_ = decoded
	}
}

// BenchmarkServer_Send measures the outbound delivery path: ServerSocket.Send of
// one message buffered and flushed to a held long-poll, for a text and a binary
// payload. A poll is held before each Send so the flush delivers immediately
// rather than leaving the packet buffered, exercising the encode-and-write path.
func BenchmarkServer_Send(b *testing.B) {
	cases := map[string]struct {
		data     []byte
		isBinary bool
	}{
		"text":   {data: []byte("hello world"), isBinary: false},
		"binary": {data: bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64), isBinary: true},
	}

	for name, tt := range cases {
		b.Run(name, func(b *testing.B) {
			server, socket, target := benchHandshakenSession(b)

			b.ReportAllocs()
			for b.Loop() {
				// Hold a poll so the buffered packet is flushed to the client
				// synchronously; the GET returns once Send delivers the payload.
				var done = make(chan struct{})
				go func() {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodGet, target, nil)
					server.ServeHTTP(rec, req)
					close(done)
				}()

				require.NoError(b, socket.Send(tt.data, tt.isBinary))
				<-done
			}
		})
	}
}

// BenchmarkServer_PollRoundTrip measures a full long-poll delivery cycle: queue
// one message with Send, then issue the GET that drains it through ServeHTTP and
// returns the encoded payload. It is the per-message server cost a polling client
// pays end to end, including the HTTP request handling around the codec.
func BenchmarkServer_PollRoundTrip(b *testing.B) {
	server, socket, target := benchHandshakenSession(b)
	data := []byte("hello world")

	b.ReportAllocs()
	for b.Loop() {
		var done = make(chan int)
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			server.ServeHTTP(rec, req)
			done <- rec.Code
		}()

		require.NoError(b, socket.Send(data, false))
		require.Equal(b, http.StatusOK, <-done)
	}
}

// BenchmarkServer_PollRoundTrip_Compressed measures a poll round trip whose body
// is large enough to be gzip-compressed, exercising the pooled gzip writer.
func BenchmarkServer_PollRoundTrip_Compressed(b *testing.B) {
	server, socket, target := benchHandshakenSession(b)
	data := bytes.Repeat([]byte("engine.io "), 256)

	b.ReportAllocs()
	for b.Loop() {
		var done = make(chan int)
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			server.ServeHTTP(rec, req)
			done <- rec.Code
		}()

		require.NoError(b, socket.Send(data, false))
		require.Equal(b, http.StatusOK, <-done)
	}
}

// benchHandshakenSession spins up a server, performs one polling handshake, and
// returns the server, its single session socket, and the polling GET/POST target
// URL for that session. The heartbeat is pushed out of the way so it never fires
// during a benchmark run. It is a helper for the outbound server benchmarks.
func benchHandshakenSession(b *testing.B) (*engineio.Server, *engineio.ServerSocket, string) {
	b.Helper()

	// Capture the session socket from the connection handler so the benchmark can
	// drive Send directly rather than racing the dispatch through HTTP.
	sockets := make(chan *engineio.ServerSocket, 1)
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Hour),
		engineio.WithPingTimeout(time.Hour),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		sockets <- socket
	})
	b.Cleanup(server.Close)

	handshake := httptest.NewRecorder()
	server.ServeHTTP(handshake, httptest.NewRequest(http.MethodGet, "/engine.io/?EIO=4&transport=polling", nil))
	require.Equal(b, http.StatusOK, handshake.Code)

	var open engineio.OpenPacket
	require.NoError(b, json.Unmarshal(handshake.Body.Bytes()[1:], &open))

	socket := <-sockets
	target := "/engine.io/?EIO=4&transport=polling&sid=" + open.SessionID

	return server, socket, target
}

// BenchmarkServer_HandleSend measures the inbound message dispatch path: a POST
// of one message packet through ServeHTTP against a handshaken session.
func BenchmarkServer_HandleSend(b *testing.B) {
	// Use a long ping interval so the heartbeat never fires during the run.
	server := engineio.NewServer(
		engineio.WithPingInterval(time.Hour),
		engineio.WithPingTimeout(time.Hour),
	)
	server.OnConnection(func(socket *engineio.ServerSocket) {
		socket.OnMessage(func(_ []byte, _ bool) {})
	})
	b.Cleanup(server.Close)

	// Perform the handshake once and capture the negotiated session id. The
	// handshake body is the open packet on the wire: a '0' type byte then JSON.
	handshake := httptest.NewRecorder()
	server.ServeHTTP(handshake, httptest.NewRequest(http.MethodGet, "/engine.io/?EIO=4&transport=polling", nil))
	require.Equal(b, http.StatusOK, handshake.Code)

	var open engineio.OpenPacket
	require.NoError(b, json.Unmarshal(handshake.Body.Bytes()[1:], &open))

	target := "/engine.io/?EIO=4&transport=polling&sid=" + open.SessionID
	body := engineio.EncodePayload([]engineio.Packet{benchSmallText})

	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		server.ServeHTTP(rec, req)
		require.Equal(b, http.StatusOK, rec.Code)
	}
}
