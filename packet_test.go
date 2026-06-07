package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestPacket_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		packet engineio.Packet
		want   string
	}{
		{
			name:   "text message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte("Hello, World!")},
			want:   "Packet{Type: message, Data: Hello, World!}",
		},
		{
			name:   "binary message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0xff}, IsBinary: true},
			want:   "Packet{Type: message, Binary: 0102ff}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Assert: the string representation matches the expectation
			require.Equal(t, tt.want, tt.packet.String())
		})
	}
}

func TestPacketType_String(t *testing.T) {
	t.Parallel()

	// Assert: the string representation of the packet type is the expected string
	require.Equal(t, "open", engineio.PacketOpen.String())
	require.Equal(t, "close", engineio.PacketClose.String())
	require.Equal(t, "ping", engineio.PacketPing.String())
	require.Equal(t, "pong", engineio.PacketPong.String())
	require.Equal(t, "message", engineio.PacketMessage.String())
	require.Equal(t, "upgrade", engineio.PacketUpgrade.String())
	require.Equal(t, "noop", engineio.PacketNoop.String())
	require.Equal(t, "unknown", engineio.PacketType(100).String())
}

func TestPacketType_Byte(t *testing.T) {
	t.Parallel()

	// Assert: the byte representation of the packet type is the expected byte
	require.Equal(t, byte('0'), engineio.PacketOpen.Byte())
	require.Equal(t, byte('1'), engineio.PacketClose.Byte())
	require.Equal(t, byte('2'), engineio.PacketPing.Byte())
	require.Equal(t, byte('3'), engineio.PacketPong.Byte())
	require.Equal(t, byte('4'), engineio.PacketMessage.Byte())
	require.Equal(t, byte('5'), engineio.PacketUpgrade.Byte())
	require.Equal(t, byte('6'), engineio.PacketNoop.Byte())
}

func TestPacketTypeFromByte(t *testing.T) {
	t.Parallel()

	// Assert: the packet type from the byte is the expected packet type
	require.Equal(t, engineio.PacketOpen, engineio.PacketTypeFromByte(byte('0')))
	require.Equal(t, engineio.PacketClose, engineio.PacketTypeFromByte(byte('1')))
	require.Equal(t, engineio.PacketPing, engineio.PacketTypeFromByte(byte('2')))
	require.Equal(t, engineio.PacketPong, engineio.PacketTypeFromByte(byte('3')))
	require.Equal(t, engineio.PacketMessage, engineio.PacketTypeFromByte(byte('4')))
	require.Equal(t, engineio.PacketUpgrade, engineio.PacketTypeFromByte(byte('5')))
	require.Equal(t, engineio.PacketNoop, engineio.PacketTypeFromByte(byte('6')))
}

func TestPacketTypeFromInt(t *testing.T) {
	t.Parallel()

	// Assert: the packet type from the integer is the expected packet type
	require.Equal(t, engineio.PacketOpen, engineio.PacketTypeFromInt(0))
	require.Equal(t, engineio.PacketClose, engineio.PacketTypeFromInt(1))
	require.Equal(t, engineio.PacketPing, engineio.PacketTypeFromInt(2))
	require.Equal(t, engineio.PacketPong, engineio.PacketTypeFromInt(3))
	require.Equal(t, engineio.PacketMessage, engineio.PacketTypeFromInt(4))
	require.Equal(t, engineio.PacketUpgrade, engineio.PacketTypeFromInt(5))
	require.Equal(t, engineio.PacketNoop, engineio.PacketTypeFromInt(6))
}
