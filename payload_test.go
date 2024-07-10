package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestEncodePayload(t *testing.T) {
	t.Parallel()

	// Arrange: create packets
	packets := []engineio.Packet{
		{Type: engineio.PacketMessage, Data: []byte("Hello")},
		{Type: engineio.PacketMessage, Data: []byte("World")},
	}

	// Act: encode packets
	encoded, err := engineio.EncodePayload(packets)
	require.NoError(t, err, "should not return an error")

	// Assert: encoded result should not be empty
	require.NotEmpty(t, encoded, "should return non-empty result")

	// Assert: encoded result should be correct
	expectedPayload := []byte{0x34, 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x1e, 0x34, 0x57, 0x6f, 0x72, 0x6c, 0x64}
	require.Equal(t, expectedPayload, encoded, "should return correct encoded result")
}

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	// Arrange: create input
	input := []byte{0x34, 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x1e, 0x34, 0x57, 0x6f, 0x72, 0x6c, 0x64}

	// Act: decode packets
	decoded, err := engineio.DecodePayload(input)
	require.NoError(t, err, "should not return an error")

	// Assert: decoded result should not be empty
	require.NotEmpty(t, decoded, "should return non-empty result")
	require.Len(t, decoded, 2, "should return 2 packets")

	// Assert: decoded packets should be correct
	require.Equal(t, engineio.PacketMessage, decoded[0].Type, "should return PacketMessage")
	require.Equal(t, []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f}, decoded[0].Data, "should return 'Hello'")

	require.Equal(t, engineio.PacketMessage, decoded[1].Type, "should return PacketMessage")
	require.Equal(t, []byte{0x57, 0x6f, 0x72, 0x6c, 0x64}, decoded[1].Data, "should return 'World'")
}
