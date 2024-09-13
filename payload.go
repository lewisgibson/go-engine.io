package engineio

import (
	"bytes"
)

// Separator is the separator for packets.
//
// https://github.com/socketio/engine.io-protocol?tab=readme-ov-file#http-long-polling-1
const Separator = '\x1E'

// EncodePayload encodes packets into bytes.
func EncodePayload(packets []Packet) []byte {
	encoded := make([][]byte, len(packets))
	for i, packet := range packets {
		encoded[i] = EncodePacket(packet)
	}
	return bytes.Join(encoded, []byte{Separator})
}

// DecodePayload decodes bytes into packets.
func DecodePayload(input []byte) ([]Packet, error) {
	packets := bytes.Split(input, []byte{Separator})

	decoded := make([]Packet, len(packets))
	for i, packet := range packets {
		p, err := DecodePacket(packet)
		if err != nil {
			return nil, err
		}
		decoded[i] = p
	}

	return decoded, nil
}
