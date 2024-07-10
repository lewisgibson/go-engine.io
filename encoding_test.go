package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestEncodePacket(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	packet := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!"),
	}

	// Act: encode the packet
	encodedPacket, err := engineio.EncodePacket(packet)
	require.NoError(t, err)

	// Assert: the encoded packet is the expected packet
	expectedPacket := []byte{0x34, 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x2c, 0x20, 0x57, 0x6f, 0x72, 0x6c, 0x64, 0x21}
	require.Equal(t, expectedPacket, encodedPacket)
}

func TestEncodePacket_Binary(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	packet := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!\n"),
	}

	// Act: encode the packet
	encodedPacket, err := engineio.EncodePacket(packet)
	require.NoError(t, err)

	// Assert: the encoded packet is the expected packet
	expectedPacket := []byte{0x62, 0x53, 0x47, 0x56, 0x73, 0x62, 0x47, 0x38, 0x73, 0x49, 0x46, 0x64, 0x76, 0x63, 0x6d, 0x78, 0x6b, 0x49, 0x51, 0x6f, 0x3d}
	require.Equal(t, expectedPacket, encodedPacket)
}

func TestDecodePacket(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	input := []byte{0x34, 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x2c, 0x20, 0x57, 0x6f, 0x72, 0x6c, 0x64, 0x21}

	// Act: decode the packet
	decodedPacket, err := engineio.DecodePacket(input)
	require.NoError(t, err)

	// Assert: the decoded packet is the same
	expectedPacket := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!"),
	}
	require.Equal(t, expectedPacket, decodedPacket)
}

func TestDecodePacket_Binary(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	input := []byte{0x62, 0x53, 0x47, 0x56, 0x73, 0x62, 0x47, 0x38, 0x73, 0x49, 0x46, 0x64, 0x76, 0x63, 0x6d, 0x78, 0x6b, 0x49, 0x51, 0x6f, 0x3d}

	// Act: decode the packet
	decodedPacket, err := engineio.DecodePacket(input)
	require.NoError(t, err)

	// Assert: the decoded packet is the same
	expectedPacket := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!\n"),
	}
	require.Equal(t, expectedPacket, decodedPacket)
}

func TestEncodeDecode(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	packet := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!"),
	}

	// Act: encode the packet
	encodedPacket, err := engineio.EncodePacket(packet)
	require.NoError(t, err)

	// Act: decode the packet
	decodedPacket, err := engineio.DecodePacket(encodedPacket)
	require.NoError(t, err)

	// Assert: the decoded packet is the same as the original packet
	require.Equal(t, packet, decodedPacket)
}

func TestEncodeDecode_Binary(t *testing.T) {
	t.Parallel()

	// Arrange: create a packet
	packet := engineio.Packet{
		Type: engineio.PacketMessage,
		Data: []byte("Hello, World!\n"),
	}

	// Act: encode the packet
	encodedPacket, err := engineio.EncodePacket(packet)
	require.NoError(t, err)

	// Act: decode the packet
	decodedPacket, err := engineio.DecodePacket(encodedPacket)
	require.NoError(t, err)

	// Assert: the decoded packet is the same as the original packet
	require.Equal(t, packet, decodedPacket)
}
