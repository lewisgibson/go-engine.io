package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestEncodePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		packets []engineio.Packet
		want    []byte
	}{
		{
			name: "text packets",
			packets: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketPing},
				{Type: engineio.PacketMessage, Data: []byte("world")},
			},
			want: []byte("4hello\x1e2\x1e4world"),
		},
		{
			name: "text and binary packets",
			packets: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			},
			want: []byte("4hello\x1ebAQIDBA=="),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: encode the payload
			encoded := engineio.EncodePayload(tt.packets)

			// Assert: the encoding matches the expected wire bytes
			require.Equal(t, tt.want, encoded)
		})
	}
}

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	// The euro sign U+20AC is one rune but three UTF-8 bytes (0xe2 0x82 0xac);
	// the v2/v3 string framing counts runes, which is the canonical trap these
	// vectors guard. It is written as \u20ac to keep the source ASCII-only.
	tests := []struct {
		name    string
		version engineio.ProtocolVersion
		input   []byte
		want    []engineio.Packet
	}{
		{
			name:    "v4 record separator",
			version: engineio.ProtocolVersion4,
			input:   []byte("4hello\x1e2\x1e4world"),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketPing, Data: []byte{}},
				{Type: engineio.PacketMessage, Data: []byte("world")},
			},
		},
		{
			name:    "v4 with binary",
			version: engineio.ProtocolVersion4,
			input:   []byte("4hello\x1ebAQIDBA=="),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			},
		},
		{
			name:    "v3 string framing with multibyte rune",
			version: engineio.ProtocolVersion3,
			input:   []byte("6:4hello2:4\u20ac"),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketMessage, Data: []byte("\u20ac")},
			},
		},
		{
			name:    "v3 base64 in string framing",
			version: engineio.ProtocolVersion3,
			input:   []byte("2:4\u20ac10:b4AQIDBA=="),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("\u20ac")},
				{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			},
		},
		{
			name:    "v3 binary framing",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x00, 0x04, 0xff, 0x34, 0xe2, 0x82, 0xac, 0x01, 0x05, 0xff, 0x04, 0x01, 0x02, 0x03, 0x04},
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("\u20ac")},
				{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			},
		},
		{
			name:    "v2 string framing",
			version: engineio.ProtocolVersion2,
			input:   []byte("6:4hello6:4world"),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte("hello")},
				{Type: engineio.PacketMessage, Data: []byte("world")},
			},
		},
		{
			name:    "v2 base64 in string framing",
			version: engineio.ProtocolVersion2,
			input:   []byte("10:b4AQIDBA=="),
			want: []engineio.Packet{
				{Type: engineio.PacketMessage, Data: []byte{0x01, 0x02, 0x03, 0x04}, IsBinary: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: decode the payload
			decoded, err := engineio.DecodePayload(tt.version, tt.input)
			require.NoError(t, err)

			// Assert: the decoded packets match the expectation
			require.Equal(t, tt.want, decoded)
		})
	}
}

func TestDecodePayload_EmptyInput(t *testing.T) {
	t.Parallel()

	t.Run("v4 returns an error", func(t *testing.T) {
		t.Parallel()

		// Act: decode an empty v4 payload
		decoded, err := engineio.DecodePayload(engineio.ProtocolVersion4, []byte{})

		// Assert: an empty-packet error is returned
		require.ErrorIs(t, err, engineio.ErrEmptyPacket)
		require.Empty(t, decoded)
	})

	for _, tt := range []struct {
		name    string
		version engineio.ProtocolVersion
	}{
		{"v2 empty payload returns no packets", engineio.ProtocolVersion2},
		{"v3 empty payload returns no packets", engineio.ProtocolVersion3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: decode an empty legacy payload
			decoded, err := engineio.DecodePayload(tt.version, []byte{})

			// Assert: no packets and no error
			require.NoError(t, err)
			require.Empty(t, decoded)
		})
	}
}

func TestDecodePayload_Malformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version engineio.ProtocolVersion
		input   []byte
		wantErr error
	}{
		{
			name:    "v4 invalid packet type",
			version: engineio.ProtocolVersion4,
			input:   []byte("4hello\x1e7"),
			wantErr: engineio.ErrInvalidPacketType,
		},
		{
			name:    "v3 missing length separator",
			version: engineio.ProtocolVersion3,
			input:   []byte("4hello"),
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 length exceeds payload",
			version: engineio.ProtocolVersion3,
			input:   []byte("9:4hello"),
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v2 rejects binary framing",
			version: engineio.ProtocolVersion2,
			input:   []byte{0x00, 0x04, 0xff, 0x34, 0x68, 0x65, 0x79},
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "unsupported version",
			version: engineio.ProtocolVersion(1),
			input:   []byte("4hello"),
			wantErr: engineio.ErrUnsupportedProtocolVersion,
		},
		{
			name:    "v3 string invalid length",
			version: engineio.ProtocolVersion3,
			input:   []byte("x:4hi"),
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 string truncated binary marker",
			version: engineio.ProtocolVersion3,
			input:   []byte("1:b"),
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 string binary invalid type",
			version: engineio.ProtocolVersion3,
			input:   []byte("2:bx"),
			wantErr: engineio.ErrInvalidPacketType,
		},
		{
			name:    "v3 string invalid type",
			version: engineio.ProtocolVersion3,
			input:   []byte("1:7"),
			wantErr: engineio.ErrInvalidPacketType,
		},
		{
			name:    "v3 binary empty packet body",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x01, 0x00, 0xff},
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 binary invalid type byte",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x01, 0x01, 0xff, 0x07},
			wantErr: engineio.ErrInvalidPacketType,
		},
		{
			name:    "v3 binary invalid length digit",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x00, 0x0a, 0xff, 0x34},
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 binary missing length terminator",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x00, 0x04},
			wantErr: engineio.ErrMalformedPayload,
		},
		{
			name:    "v3 binary length exceeds payload",
			version: engineio.ProtocolVersion3,
			input:   []byte{0x00, 0x04, 0xff, 0x34},
			wantErr: engineio.ErrMalformedPayload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act: decode the malformed payload
			decoded, err := engineio.DecodePayload(tt.version, tt.input)

			// Assert: the expected error is returned and no packets escape
			require.ErrorIs(t, err, tt.wantErr)
			require.Nil(t, decoded)
		})
	}
}

func TestDecodePayload_InvalidBase64Segment(t *testing.T) {
	t.Parallel()

	// Act: decode a v3 string-framed binary segment with invalid base64
	decoded, err := engineio.DecodePayload(engineio.ProtocolVersion3, []byte("4:b4@@"))

	// Assert: an error is returned and no packets escape
	require.Error(t, err)
	require.Nil(t, decoded)
}
