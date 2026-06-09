package engineio

import (
	"encoding/base64"
	"errors"
	"fmt"
)

// Sentinel Errors.
var (
	ErrEmptyPacket       = errors.New("empty packet")
	ErrInvalidPacketType = errors.New("invalid packet type")
)

// BinaryMarker is the byte that prefixes a base64-encoded binary packet.
//
// https://github.com/socketio/engine.io-protocol#packet-encoding
const BinaryMarker = 'b'

// EncodePacket encodes a packet into its text wire form. A binary message is
// base64-encoded behind the BinaryMarker; any other packet is its type byte
// followed by its data. Binary messages sent over a transport with native
// binary frames (WebSocket) are written as raw frames and never pass here.
func EncodePacket(packet Packet) []byte {
	return appendPacket(nil, packet)
}

// appendPacket appends packet's text wire form to dst and returns the extended
// slice, so a caller encoding several packets can reuse one buffer instead of
// allocating a fresh slice per packet. EncodePacket is appendPacket(nil, ...).
func appendPacket(dst []byte, packet Packet) []byte {
	if packet.IsBinary {
		return base64.StdEncoding.AppendEncode(append(dst, BinaryMarker), packet.Data)
	}

	return append(append(dst, packet.Type.Byte()), packet.Data...)
}

// DecodePacket decodes a single packet from its text wire form. A leading
// BinaryMarker denotes a base64-encoded binary message; otherwise the first
// byte is the packet type and the remainder is the data.
func DecodePacket(input []byte) (Packet, error) {
	switch {
	case len(input) == 0:
		return Packet{}, ErrEmptyPacket

	// A binary packet carries no type digit; the marker itself implies a message.
	case input[0] == BinaryMarker:
		data, err := base64.StdEncoding.DecodeString(string(input[1:]))
		if err != nil {
			return Packet{}, fmt.Errorf("decoding base64: %w", err)
		}
		return Packet{Type: PacketMessage, Data: data, IsBinary: true}, nil
	}

	// With no binary marker, the first byte is the packet type and the remainder is
	// the data.
	var packetType = PacketTypeFromByte(input[0])
	if !packetType.valid() {
		return Packet{}, fmt.Errorf("%w: %q", ErrInvalidPacketType, input[0])
	}

	return Packet{Type: packetType, Data: input[1:]}, nil
}
