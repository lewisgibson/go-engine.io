package engineio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Sentinel Errors.
var (
	// errWebTransportFrameTooLarge is returned when a frame declares a length above
	// the negotiated maximum payload, matching the reference parser's rejection of
	// oversized frames before they are buffered.
	errWebTransportFrameTooLarge = errors.New("webtransport frame exceeds max payload")
)

// WebTransport carries every Engine.IO packet on one bidirectional stream, each
// framed with a WebSocket-style length prefix: a header byte whose top bit flags
// a binary payload and whose low seven bits hold the length or a marker (126 ->
// a uint16 length, 127 -> a uint64 length, both big-endian), followed by the
// payload. A text payload is the packet's type byte and data; a binary message
// payload is its raw bytes, never base64. This mirrors engine.io-parser's
// createPacketEncoderStream / createPacketDecoderStream.
const (
	// webTransportLen16Marker in the length nibble announces a 16-bit big-endian
	// length in the next two bytes.
	webTransportLen16Marker = 126
	// webTransportLen64Marker announces a 64-bit big-endian length in the next
	// eight bytes.
	webTransportLen64Marker = 127
	// webTransportBinaryFlag is the header bit set when the payload is a raw binary
	// message rather than a text frame.
	webTransportBinaryFlag = 0x80
)

// appendWebTransportFrame appends the framed wire form of packet to dst and
// returns the extended slice, so a caller delivering several packets can reuse a
// single buffer instead of allocating one per frame.
func appendWebTransportFrame(dst []byte, packet Packet) []byte {
	// A binary message rides as raw bytes with its flag bit set; every other packet
	// is the text form (type byte followed by data) the codec emits elsewhere, so the
	// peer decodes it exactly as it would a websocket text frame. The flag is folded
	// into the first header byte as it is written, which avoids tracking its index.
	var payload []byte
	var flag byte
	if packet.IsBinary {
		payload = packet.Data
		flag = webTransportBinaryFlag
	} else {
		payload = EncodePacket(packet)
	}

	length := len(payload)
	switch {
	case length < webTransportLen16Marker:
		dst = append(dst, byte(length)|flag)

	case length < 1<<16:
		dst = append(dst, webTransportLen16Marker|flag)
		dst = binary.BigEndian.AppendUint16(dst, uint16(length))

	default:
		dst = append(dst, webTransportLen64Marker|flag)
		dst = binary.BigEndian.AppendUint64(dst, uint64(length))
	}

	return append(dst, payload...)
}

// readWebTransportPacket reads one framed packet from r. It rejects a frame larger
// than maxPayload (maxPayload <= 0 disables the ceiling), so a peer cannot force an
// unbounded allocation. A zero-length frame is accepted as an empty binary message
// (an empty text frame is still rejected downstream by DecodePacket). It returns
// io.EOF when the stream ends cleanly between frames, and io.ErrUnexpectedEOF when
// it ends mid-frame.
func readWebTransportPacket(r io.Reader, maxPayload int) (Packet, error) {
	var header [1]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Packet{}, err
	}

	isBinary := header[0]&webTransportBinaryFlag != 0

	// The low seven bits are the length, or a marker selecting an extended length
	// in the bytes that follow.
	var length uint64
	switch marker := header[0] &^ webTransportBinaryFlag; {
	case marker < webTransportLen16Marker:
		length = uint64(marker)

	case marker == webTransportLen16Marker:
		var extended [2]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Packet{}, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))

	default:
		var extended [8]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return Packet{}, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}

	// Reject a frame larger than the negotiated maximum before allocating its
	// payload, so a peer cannot force an unbounded read.
	if maxPayload > 0 && length > uint64(maxPayload) {
		return Packet{}, fmt.Errorf("%w: %d > %d", errWebTransportFrameTooLarge, length, maxPayload)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Packet{}, err
	}

	// A flagged frame is a raw binary message, a zero-length one being a valid empty
	// message; an unflagged frame is the text wire form. (The reference parser instead
	// rejects a zero-length frame as a protocol error, so an empty binary message sent
	// to a reference peer such as a browser is dropped there; two go-engine.io peers
	// round-trip it.)
	if isBinary {
		return Packet{Type: PacketMessage, Data: payload, IsBinary: true}, nil
	}

	return DecodePacket(payload)
}
