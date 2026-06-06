package engineio

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Sentinel Errors.
var (
	ErrMalformedPayload           = errors.New("malformed payload")
	ErrUnsupportedProtocolVersion = errors.New("unsupported protocol version")
)

const (
	// Separator joins packets in a v4 long-polling payload (record separator).
	//
	// https://github.com/socketio/engine.io-protocol#http-long-polling-1
	Separator = '\x1e'

	// v2 and v3 binary payload framing prefixes each packet with a marker byte
	// (0x00 for a following string packet, 0x01 for a following binary packet),
	// the byte-count length written as one value byte per decimal digit, and a
	// 0xff terminator.
	binaryStringMarker     byte = 0x00
	binaryBinaryMarker     byte = 0x01
	binaryLengthTerminator byte = 0xff
)

// EncodePayload encodes packets into a v4 long-polling payload. Encoding is
// v4-only: this library always speaks v4, so it never needs to produce the
// legacy v2/v3 framings (it only decodes them, via DecodePayload).
func EncodePayload(packets []Packet) []byte {
	var encoded = make([][]byte, len(packets))
	for i, packet := range packets {
		encoded[i] = EncodePacket(packet)
	}

	return bytes.Join(encoded, []byte{Separator})
}

// DecodePayload decodes a long-polling payload body into packets according to
// the negotiated protocol version:
//
//   - v4 concatenates packets with the record separator (0x1e).
//   - v3 uses either the length-prefixed string framing ("<len>:<packet>") or
//     the binary framing (0x00/0x01 marker, value-byte length, 0xff), selected
//     by the first byte.
//   - v2 uses the string framing only; a binary frame marker is malformed.
func DecodePayload(version ProtocolVersion, input []byte) ([]Packet, error) {
	switch version {
	case ProtocolVersion4:
		return decodeRecordSeparatorPayload(input)

	case ProtocolVersion3:
		// The string framing always begins with an ASCII length digit, the
		// binary framing with a 0x00/0x01 marker; the ranges are disjoint, so
		// the first byte selects the machine unambiguously.
		if len(input) != 0 && (input[0] == binaryStringMarker || input[0] == binaryBinaryMarker) {
			return decodeBinaryFramedPayload(input)
		}
		return decodeStringFramedPayload(input)

	case ProtocolVersion2:
		return decodeStringFramedPayload(input)

	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedProtocolVersion, version)
	}
}

// decodeRecordSeparatorPayload decodes a v4 payload by splitting on the record
// separator and decoding each segment as a single packet.
func decodeRecordSeparatorPayload(input []byte) ([]Packet, error) {
	segments := bytes.Split(input, []byte{Separator})

	var packets = make([]Packet, 0, len(segments))
	for _, segment := range segments {
		packet, err := DecodePacket(segment)
		if err != nil {
			return nil, fmt.Errorf("decoding packet: %w", err)
		}
		packets = append(packets, packet)
	}

	return packets, nil
}

// decodeStringFramedPayload decodes a v2/v3 "<length>:<packet>" payload. The
// length counts runes (Unicode code points), not bytes, so the body is walked
// as runes; within each packet the data is its UTF-8 bytes.
func decodeStringFramedPayload(input []byte) ([]Packet, error) {
	runes := []rune(string(input))

	var packets []Packet
	for cursor := 0; cursor < len(runes); {
		var separator = slices.Index(runes[cursor:], ':')
		if separator == -1 {
			return nil, fmt.Errorf("%w: missing length separator", ErrMalformedPayload)
		}
		separator += cursor

		length, err := strconv.Atoi(string(runes[cursor:separator]))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid length: %w", ErrMalformedPayload, err)
		}

		var start = separator + 1
		var end = start + length
		if length < 0 || end > len(runes) {
			return nil, fmt.Errorf("%w: length exceeds payload", ErrMalformedPayload)
		}

		packet, err := decodeStringFramedPacket(string(runes[start:end]))
		if err != nil {
			return nil, err
		}
		packets = append(packets, packet)

		cursor = end
	}

	return packets, nil
}

// decodeStringFramedPacket decodes one v2/v3 string-framed packet. A leading
// BinaryMarker means "b<type><base64>": the type digit is kept between the
// marker and the base64 data (unlike v4, where the marker implies a message
// and no type digit follows).
func decodeStringFramedPacket(packet string) (Packet, error) {
	if packet == "" {
		return Packet{}, ErrEmptyPacket
	}

	if packet[0] == BinaryMarker {
		if len(packet) < 2 {
			return Packet{}, fmt.Errorf("%w: truncated binary packet", ErrMalformedPayload)
		}

		var packetType = PacketTypeFromByte(packet[1])
		if !packetType.valid() {
			return Packet{}, fmt.Errorf("%w: %q", ErrInvalidPacketType, packet[1])
		}

		data, err := base64.StdEncoding.DecodeString(packet[2:])
		if err != nil {
			return Packet{}, fmt.Errorf("decoding base64: %w", err)
		}

		return Packet{Type: packetType, Data: data, IsBinary: true}, nil
	}

	var packetType = PacketTypeFromByte(packet[0])
	if !packetType.valid() {
		return Packet{}, fmt.Errorf("%w: %q", ErrInvalidPacketType, packet[0])
	}

	return Packet{Type: packetType, Data: []byte(packet[1:])}, nil
}

// decodeBinaryFramedPayload decodes a v3 binary-framed payload. Each packet is
// a marker byte (0x00 string, 0x01 binary), a byte-count length written one
// value byte per decimal digit, a 0xff terminator, then the packet body.
func decodeBinaryFramedPayload(input []byte) ([]Packet, error) {
	var packets []Packet
	for cursor := 0; cursor < len(input); {
		var marker = input[cursor]
		if marker != binaryStringMarker && marker != binaryBinaryMarker {
			return nil, fmt.Errorf("%w: invalid frame marker %#x", ErrMalformedPayload, marker)
		}
		cursor++

		var length int
		var terminated bool
		for cursor < len(input) {
			digit := input[cursor]
			cursor++
			if digit == binaryLengthTerminator {
				terminated = true
				break
			}
			if digit > 9 {
				return nil, fmt.Errorf("%w: invalid length digit %#x", ErrMalformedPayload, digit)
			}
			length = length*10 + int(digit)
		}
		if !terminated {
			return nil, fmt.Errorf("%w: missing length terminator", ErrMalformedPayload)
		}

		var end = cursor + length
		if end > len(input) {
			return nil, fmt.Errorf("%w: length exceeds payload", ErrMalformedPayload)
		}
		body := input[cursor:end]
		cursor = end

		packet, err := decodeBinaryFramedPacket(marker, body)
		if err != nil {
			return nil, err
		}
		packets = append(packets, packet)
	}

	return packets, nil
}

// decodeBinaryFramedPacket decodes one v3 binary-framed packet body. A string
// marker carries UTF-8 "<type-char><data>"; a binary marker carries a raw type
// byte (0x04 for a message) followed by the raw payload bytes.
func decodeBinaryFramedPacket(marker byte, body []byte) (Packet, error) {
	if marker == binaryStringMarker {
		return decodeStringFramedPacket(string(body))
	}

	if len(body) == 0 {
		return Packet{}, fmt.Errorf("%w: empty binary packet", ErrMalformedPayload)
	}

	var packetType = PacketType(body[0])
	if !packetType.valid() {
		return Packet{}, fmt.Errorf("%w: %#x", ErrInvalidPacketType, body[0])
	}

	return Packet{Type: packetType, Data: body[1:], IsBinary: true}, nil
}
