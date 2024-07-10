package engineio

// Packet represents a packet.
type Packet struct {
	Type PacketType
	Data []byte
}

// PacketType is the type of the packet.
// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#handshake
type PacketType uint8

const (
	// Used during the handshake.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#handshake
	PacketOpen = PacketType(iota)

	// Used to indicate that a transport can be closed.
	PacketClose

	// Used in the heartbeat mechanism.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PacketPing

	// Used in the heartbeat mechanism.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PacketPong

	// Used to send a payload to the other side.
	PacketMessage

	// Used during the upgrade process.
	// https://github.com/socketio/engine.io-protocol#upgrade
	PacketUpgrade

	// Used during the upgrade process.
	// https://github.com/socketio/engine.io-protocol#upgrade
	PacketNoop
)

// String returns the string representation of the packet type.
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

// Byte returns the byte representation of the packet type.
func (p PacketType) Byte() byte {
	return byte(p) + '0'
}

// PacketTypeFromByte converts the packet type value as a byte into a PacketType.
func PacketTypeFromByte(b byte) PacketType {
	return PacketType(b - '0')
}

// PacketTypeFromInt converts the packet type value as an unsigned integer into a PacketType.
func PacketTypeFromInt(u uint8) PacketType {
	return PacketType(u)
}

// OpenPacket represents the data for a packet with the type PacketOpen.
//
// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#handshake
type OpenPacket struct {
	// The session ID.
	SessionID string `json:"sid"`

	// The list of available transport upgrades.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#upgrade
	Upgrades []TransportType `json:"upgrades"`

	// The ping interval, used in the heartbeat mechanism (in milliseconds).
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PingInterval int `json:"pingInterval"`

	// The ping timeout, used in the heartbeat mechanism (in milliseconds).
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#heartbeat
	PingTimeout int `json:"pingTimeout"`

	// The maximum number of bytes per chunk, used by the client to aggregate packets into payloads.
	// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#packet-encoding
	MaxPayload int `json:"maxPayload"`
}
