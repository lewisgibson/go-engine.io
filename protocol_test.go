package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

// TestProtocol_Version pins the protocol version constants so an accidental
// change to the negotiated EIO value is caught at compile-and-test time.
func TestProtocol_Version(t *testing.T) {
	t.Parallel()

	// Assert: the library speaks v4, and the version constants keep their wire values
	require.Equal(t, engineio.ProtocolVersion4, engineio.Protocol)
	require.Equal(t, engineio.ProtocolVersion2, engineio.ProtocolVersion(2))
	require.Equal(t, engineio.ProtocolVersion3, engineio.ProtocolVersion(3))
	require.Equal(t, engineio.ProtocolVersion4, engineio.ProtocolVersion(4))
}

// TestProtocol_PacketTypeWireValues pins the seven Engine.IO packet types to
// their typed ordinals (PacketOpen==0 .. PacketNoop==6) and their wire encoding
// (ASCII '0'..'6'), so a reorder of the constant block is caught.
func TestProtocol_PacketTypeWireValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		packet   engineio.PacketType
		ordinal  uint8
		wireByte byte
	}{
		{name: "open", packet: engineio.PacketOpen, ordinal: 0, wireByte: '0'},
		{name: "close", packet: engineio.PacketClose, ordinal: 1, wireByte: '1'},
		{name: "ping", packet: engineio.PacketPing, ordinal: 2, wireByte: '2'},
		{name: "pong", packet: engineio.PacketPong, ordinal: 3, wireByte: '3'},
		{name: "message", packet: engineio.PacketMessage, ordinal: 4, wireByte: '4'},
		{name: "upgrade", packet: engineio.PacketUpgrade, ordinal: 5, wireByte: '5'},
		{name: "noop", packet: engineio.PacketNoop, ordinal: 6, wireByte: '6'},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Assert: the typed constant holds its ordinal
			require.Equal(t, engineio.PacketType(tt.ordinal), tt.packet)

			// Assert: the type encodes on the wire as its ASCII digit and round-trips
			require.Equal(t, tt.wireByte, tt.packet.Byte())
			require.Equal(t, tt.packet, engineio.PacketTypeFromByte(tt.wireByte))
		})
	}
}
