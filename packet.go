package engineio

import "fmt"

// Packet is a single Engine.IO protocol packet: a type plus its payload. It is
// the unit every transport encodes, decodes, and dispatches, so the same value
// round-trips through long-polling, WebSocket, the codec, and the handlers.
type Packet struct {
	// Type is the packet's kind, which determines how transports and handlers
	// treat it.
	Type PacketType
	// Data is the packet payload. For a text packet it holds the UTF-8 bytes;
	// for a binary message it holds the raw (already base64-decoded) bytes.
	Data []byte
	// IsBinary reports whether Data is a binary message. Engine.IO can only
	// carry binary as a message, so IsBinary being true implies Type is
	// PacketMessage. It is set explicitly rather than inferred from Data so
	// that callers and transports never have to re-sniff the bytes.
	IsBinary bool
}

// String returns a human-readable representation of the packet for logging and
// test output. Binary data is rendered as hex so it stays printable; text data
// is rendered as-is.
func (p Packet) String() string {
	if p.IsBinary {
		return fmt.Sprintf("Packet{Type: %s, Binary: %x}", p.Type, p.Data)
	}

	return fmt.Sprintf("Packet{Type: %s, Data: %s}", p.Type, p.Data)
}

// PacketType identifies which of the seven Engine.IO packet kinds a Packet is.
// On the wire it is encoded as a single ASCII digit ('0'..'6'); see Byte and
// PacketTypeFromByte for the mapping.
//
// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#protocol
type PacketType uint8

const (
	// PacketOpen carries the handshake details (session id, ping timings, and
	// offered upgrades) and is the first packet the server sends on a new session.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#handshake
	PacketOpen PacketType = iota

	// PacketClose signals that the transport can be closed; either peer may send it
	// to tear the session down gracefully.
	PacketClose

	// PacketPing is sent by the server as part of the v4 server-initiated heartbeat;
	// the client must answer with a PacketPong to keep the session alive. During an
	// upgrade probe it is also sent by the client with the "probe" payload.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PacketPing

	// PacketPong answers a PacketPing in the heartbeat. During an upgrade probe the
	// server replies to the client's "probe" ping with a "probe" pong.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PacketPong

	// PacketMessage carries application data. It is the only packet that may hold a
	// binary payload, so a Packet with IsBinary set is always a PacketMessage.
	PacketMessage

	// PacketUpgrade commits a transport upgrade: the client sends it over the
	// probing transport once the probe pong is received, telling the server to
	// switch traffic to the new transport.
	//
	// https://github.com/socketio/engine.io-protocol#upgrade
	PacketUpgrade

	// PacketNoop is a no-op used to release a held long-poll so the client can
	// finish pausing the polling transport during an upgrade.
	//
	// https://github.com/socketio/engine.io-protocol#upgrade
	PacketNoop
)

// String returns the lowercase protocol name of the packet type ("open",
// "ping", and so on), or "unknown" for an unrecognised value. It is intended
// for logging rather than wire encoding; use Byte for the wire form.
func (p PacketType) String() string {
	switch p {
	case PacketOpen:
		return "open"

	case PacketClose:
		return "close"

	case PacketPing:
		return "ping"

	case PacketPong:
		return "pong"

	case PacketMessage:
		return "message"

	case PacketUpgrade:
		return "upgrade"

	case PacketNoop:
		return "noop"

	default:
		return "unknown"
	}
}

// Byte returns the single ASCII digit that encodes the packet type on the wire
// ('0' for PacketOpen through '6' for PacketNoop). It is the inverse of
// PacketTypeFromByte.
func (p PacketType) Byte() byte {
	return byte(p) + '0'
}

// valid reports whether the packet type is one of the known Engine.IO types.
func (p PacketType) valid() bool {
	return p <= PacketNoop
}

// PacketTypeFromByte decodes the ASCII digit byte used on the wire into a
// PacketType. It is the inverse of Byte. The result is not validated here;
// callers that read untrusted input should check it with the protocol's
// validity rules before trusting the type.
func PacketTypeFromByte(b byte) PacketType {
	return PacketType(b - '0')
}

// PacketTypeFromInt converts a raw numeric value into a PacketType, for callers
// that already hold the type as an integer rather than as its ASCII wire byte.
func PacketTypeFromInt(u uint8) PacketType {
	return PacketType(u)
}

// OpenPacket is the JSON payload of a PacketOpen. The server sends it to seal
// the handshake and tell the client the session parameters it must honour; the
// client unmarshals it to learn its session id, heartbeat timings, payload
// limit, and which transports it may upgrade to.
//
// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#handshake
type OpenPacket struct {
	// SessionID is the unique identifier the server assigns to this connection.
	// The client echoes it as the "sid" query parameter on every later request.
	SessionID string `json:"sid"`

	// Upgrades lists the transports the server is willing to upgrade to. An empty
	// list means the client must stay on its current transport.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#upgrade
	Upgrades []TransportType `json:"upgrades"`

	// PingInterval is how often, in milliseconds, the server sends a ping; the
	// client uses it (with PingTimeout) to detect a silent connection.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PingInterval int `json:"pingInterval"`

	// PingTimeout is how long, in milliseconds, to wait for server activity before
	// treating the connection as dead and closing it.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PingTimeout int `json:"pingTimeout"`

	// MaxPayload is the maximum number of bytes the server accepts in a single
	// long-polling payload; the client splits its writes into chunks no larger.
	//
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#packet-encoding
	MaxPayload int `json:"maxPayload"`
}
