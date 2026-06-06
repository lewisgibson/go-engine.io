package engineio

// ProtocolVersion is an Engine.IO protocol version, sent as the EIO query
// parameter during the handshake.
type ProtocolVersion int

const (
	// ProtocolVersion2 is Engine.IO protocol v2.
	ProtocolVersion2 ProtocolVersion = 2
	// ProtocolVersion3 is Engine.IO protocol v3.
	ProtocolVersion3 ProtocolVersion = 3
	// ProtocolVersion4 is Engine.IO protocol v4, the version this library speaks.
	ProtocolVersion4 ProtocolVersion = 4
)

// Protocol is the protocol version this library implements.
const Protocol = ProtocolVersion4
