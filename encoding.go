package engineio

import (
	"encoding/base64"
	"errors"
	"unicode"
)

// ErrEmptyPacket is returned when the input is empty.
var ErrEmptyPacket = errors.New("empty packet")

// BinaryMarker is the marker for binary packets.
const BinaryMarker = 'b'

// EncodePacket encodes a packet into bytes.
func EncodePacket(packet Packet) ([]byte, error) {
	// binary is true if the data contains non-ASCII characters.
	var binary bool
	for _, r := range string(packet.Data) {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			binary = true
		}
	}

	switch {
	// The packet is a binary packet.
	case binary:
		return append(
			[]byte{BinaryMarker},
			[]byte(base64.StdEncoding.EncodeToString(packet.Data))...,
		), nil

	// The packet is a text packet.
	default:
		return append(
			[]byte{packet.Type.Byte()},
			packet.Data...,
		), nil
	}
}

// DecodePacket decodes a packet from a string.
func DecodePacket(input []byte) (Packet, error) {
	switch {
	// The input is empty.
	case len(input) == 0:
		return Packet{}, ErrEmptyPacket

	// The input is a binary packet. This must be a message packet.
	case input[0] == BinaryMarker:
		data, err := base64.StdEncoding.DecodeString(string(input[1:]))
		if err != nil {
			return Packet{}, err
		}
		return Packet{Type: PacketMessage, Data: data}, nil

	// The input is a single byte packet, this indicates no data.
	case len(input) == 1:
		return Packet{Type: PacketTypeFromByte(input[0]), Data: []byte{}}, nil

	// The input is a packet with data.
	default:
		return Packet{Type: PacketTypeFromByte(input[0]), Data: input[1:]}, nil
	}
}
