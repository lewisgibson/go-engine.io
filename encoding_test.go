package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestEncodePacket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		packet engineio.Packet
		want   []byte
	}{
		{
			name:   "text message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte("hello")},
			want:   []byte("4hello"),
		},
		{
			name:   "ping without data",
			packet: engineio.Packet{Type: engineio.PacketPing},
			want:   []byte("2"),
		},
		{
			name:   "open packet with json",
			packet: engineio.Packet{Type: engineio.PacketOpen, Data: []byte(`{"sid":"x"}`)},
			want:   []byte(`0{"sid":"x"}`),
		},
		{
			name:   "binary message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			want:   []byte("bAQIDBA=="),
		},
		{
			name:   "empty binary message",
			packet: engineio.Packet{Type: engineio.PacketMessage, IsBinary: true},
			want:   []byte("b"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: encode the packet
			encoded := engineio.EncodePacket(tt.packet)

			// Assert: the encoding matches the expected wire bytes
			require.Equal(t, tt.want, encoded)
		})
	}
}

func TestDecodePacket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
		want  engineio.Packet
	}{
		{
			name:  "text message",
			input: []byte("4hello"),
			want:  engineio.Packet{Type: engineio.PacketMessage, Data: []byte("hello")},
		},
		{
			name:  "ping without data",
			input: []byte("2"),
			want:  engineio.Packet{Type: engineio.PacketPing, Data: []byte{}},
		},
		{
			name:  "binary message",
			input: []byte("bAQIDBA=="),
			want:  engineio.Packet{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
		},
		{
			name:  "empty binary message",
			input: []byte("b"),
			want:  engineio.Packet{Type: engineio.PacketMessage, Data: []byte{}, IsBinary: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: decode the packet
			decoded, err := engineio.DecodePacket(tt.input)
			require.NoError(t, err)

			// Assert: the decoded packet matches the expectation
			require.Equal(t, tt.want, decoded)
		})
	}
}

func TestDecodePacket_EmptyInput(t *testing.T) {
	t.Parallel()

	// Act: decode an empty input
	packet, err := engineio.DecodePacket([]byte{})

	// Assert: an empty-packet error is returned
	require.ErrorIs(t, err, engineio.ErrEmptyPacket)
	require.Zero(t, packet)
}

func TestDecodePacket_InvalidType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
	}{
		{name: "out of range digit", input: []byte("7")},
		{name: "non-digit byte", input: []byte("x")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: decode the malformed packet
			packet, err := engineio.DecodePacket(tt.input)

			// Assert: an invalid-type error is returned
			require.ErrorIs(t, err, engineio.ErrInvalidPacketType)
			require.Zero(t, packet)
		})
	}
}

func TestDecodePacket_InvalidBase64(t *testing.T) {
	t.Parallel()

	// Act: decode a binary packet with invalid base64
	packet, err := engineio.DecodePacket([]byte("b@@@"))

	// Assert: an error is returned
	require.Error(t, err)
	require.Zero(t, packet)
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		packet engineio.Packet
	}{
		{
			name:   "text message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte("Hello, World!")},
		},
		{
			name:   "binary message",
			packet: engineio.Packet{Type: engineio.PacketMessage, Data: []byte{0x00, 0xff, 0x10, 0x80}, IsBinary: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: encode then decode the packet
			decoded, err := engineio.DecodePacket(engineio.EncodePacket(tt.packet))
			require.NoError(t, err)

			// Assert: the round trip preserves the packet
			require.Equal(t, tt.packet, decoded)
		})
	}
}
